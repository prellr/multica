package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// PullRequestEnriched carries the per-PR fields GitHub's REST list endpoint
// omits — mergeable and the combined check/status rollup — for a batch of
// open PRs fetched in a SINGLE GraphQL query.
//
// Ship Hub's sync used to obtain these per PR: GET /pulls/{n} for mergeable
// plus two commit-status calls (combined-status + check-runs) for CI. That
// per-PR REST fan-out is what exhausted the 5,000/hr core budget on busy
// repos (ROA-946) — a repo with 80 open PRs cost ~160 calls per reconciler
// tick. One GraphQL query returns mergeable + CI for up to 100 PRs and bills
// ~1 point against the SEPARATE GraphQL budget.
type PullRequestEnriched struct {
	Number  int
	HeadSHA string
	// Mergeable is GitHub's GraphQL tri-state: "MERGEABLE" | "CONFLICTING" |
	// "UNKNOWN". UNKNOWN means GitHub hasn't finished the async merge
	// computation yet — the same transient state the REST *bool nil holds.
	Mergeable string
	// CIStatus is the reduced rollup in the SAME vocabulary GetCIStatus
	// returns: "success" | "failure" | "pending" | "" (no checks).
	CIStatus string
}

// MergeableBool maps the GraphQL tri-state to the REST *bool convention
// (nil = UNKNOWN) so callers can feed it straight into the existing upsert
// path's mapMergeable without a second mapping table.
func (e PullRequestEnriched) MergeableBool() *bool {
	switch e.Mergeable {
	case "MERGEABLE":
		v := true
		return &v
	case "CONFLICTING":
		v := false
		return &v
	default:
		return nil
	}
}

const pullRequestsEnrichedQuery = `query($owner:String!,$repo:String!,$first:Int!){
  repository(owner:$owner,name:$repo){
    pullRequests(states:OPEN, first:$first, orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{
        number
        headRefOid
        mergeable
        commits(last:1){ nodes{ commit{ statusCheckRollup{ state } } } }
      }
    }
  }
}`

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type enrichedResponse struct {
	Data struct {
		Repository *struct {
			PullRequests struct {
				Nodes []struct {
					Number     int    `json:"number"`
					HeadRefOid string `json:"headRefOid"`
					Mergeable  string `json:"mergeable"`
					Commits    struct {
						Nodes []struct {
							Commit struct {
								StatusCheckRollup *struct {
									State string `json:"state"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

// ListPullRequestsEnriched fetches mergeable + CI rollup for up to `first`
// (capped at 100) open PRs in ONE GraphQL request. Returns ErrNotFound when
// the repo isn't visible to any configured token — the caller (SyncProject)
// then falls back to the per-PR REST enrichment path, which has its own
// App→PAT 404 fallback.
//
// Token handling mirrors the REST client: the primary token (App
// installation or PAT) is tried first; if the repo comes back null AND a
// FallbackToken (PAT) is configured, the query is retried with it — a
// GraphQL "repository: null" is the 200-status equivalent of the REST 404
// the App-doesn't-cover-this-owner case produces, so without this retry a
// cross-namespace repo would never benefit from the batch query.
func (c *Client) ListPullRequestsEnriched(ctx context.Context, owner, repo string, first int) ([]PullRequestEnriched, error) {
	if first <= 0 || first > 100 {
		first = 100
	}
	primary, err := c.primaryToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("github: token source: %w", err)
	}

	prs, visible, err := c.queryEnriched(ctx, primary, owner, repo, first)
	if err != nil {
		return nil, err
	}
	if !visible {
		if c.FallbackToken == "" || c.FallbackToken == primary {
			return nil, ErrNotFound
		}
		// Parity with doWithBody: remember the App-doesn't-cover-this-owner
		// verdict so subsequent REST calls skip the wasted App round-trip.
		if c.AppMissOwnerTTL > 0 {
			c.recordAppMissOwner(owner)
		}
		prs, visible, err = c.queryEnriched(ctx, c.FallbackToken, owner, repo, first)
		if err != nil {
			return nil, err
		}
		if !visible {
			return nil, ErrNotFound
		}
	}
	return prs, nil
}

// queryEnriched runs one GraphQL request with an explicit token. visible is
// false when the response's data.repository is null (repo not accessible to
// this token) — distinct from a hard error, so the caller can decide whether
// to retry with a different token.
func (c *Client) queryEnriched(ctx context.Context, token, owner, repo string, first int) (prs []PullRequestEnriched, visible bool, err error) {
	reqBody := map[string]any{
		"query": pullRequestsEnrichedQuery,
		"variables": map[string]any{
			"owner": owner,
			"repo":  repo,
			"first": first,
		},
	}
	var out enrichedResponse
	if err := c.postGraphQL(ctx, token, reqBody, &out); err != nil {
		return nil, false, err
	}
	for _, e := range out.Errors {
		if e.Type == "RATE_LIMITED" {
			return nil, false, ErrRateLimited
		}
		// NOT_FOUND surfaces as data.repository == null below; any other
		// typed error (malformed query, FORBIDDEN/SAML, etc.) is a real
		// failure the caller must see rather than silently treat as empty.
		if e.Type != "NOT_FOUND" {
			return nil, false, fmt.Errorf("github: graphql: %s", e.Message)
		}
	}
	if out.Data.Repository == nil {
		return nil, false, nil
	}
	nodes := out.Data.Repository.PullRequests.Nodes
	prs = make([]PullRequestEnriched, 0, len(nodes))
	for _, n := range nodes {
		var rollup string
		if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
			rollup = n.Commits.Nodes[0].Commit.StatusCheckRollup.State
		}
		prs = append(prs, PullRequestEnriched{
			Number:    n.Number,
			HeadSHA:   n.HeadRefOid,
			Mergeable: n.Mergeable,
			CIStatus:  mapRollupState(rollup),
		})
	}
	return prs, true, nil
}

// postGraphQL POSTs a GraphQL request body with an EXPLICIT bearer token,
// reusing the client's HTTP client, the rate-limit observer (the response
// carries X-RateLimit-Resource: graphql, so it's tracked against the
// separate GraphQL budget), and the shared response classifier. Unlike
// doWithBody it takes the token explicitly, because the GraphQL App→PAT
// fallback keys on a null repository in a 200 body rather than an HTTP 404,
// so the doWithBody retry loop can't drive it.
func (c *Client) postGraphQL(ctx context.Context, token string, reqBody, target any) error {
	base := c.BaseURL
	if base == "" {
		base = apiBase
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("github: encode graphql body: %w", err)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/graphql", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	c.rateLimit.observeResponse(http.MethodPost, "/graphql", resp.Header)
	return c.classifyResponse(http.MethodPost, "/graphql", resp, body, target)
}

// primaryToken resolves the token doWithBody would use as its primary: the
// TokenSource when configured, else the static Token.
func (c *Client) primaryToken(ctx context.Context) (string, error) {
	if c.TokenSource != nil {
		return c.TokenSource.Token(ctx)
	}
	return c.Token, nil
}

// mapRollupState reduces GitHub's GraphQL StatusState enum to the same
// vocabulary GetCIStatus produces, so the GraphQL and REST CI paths write
// identical ci_status values. EXPECTED is a status context that's been
// created but not yet reported — treated as pending. A nil rollup (repo
// with no checks at all) yields "".
func mapRollupState(state string) string {
	switch state {
	case "SUCCESS":
		return "success"
	case "FAILURE", "ERROR":
		return "failure"
	case "PENDING", "EXPECTED":
		return "pending"
	default:
		return ""
	}
}

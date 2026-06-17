package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// graphQLServer stands up an httptest server that answers POST /graphql with
// the supplied JSON body, recording the Authorization header of each call so
// tests can assert which token was used.
func graphQLServer(t *testing.T, bodyForToken func(auth string) string) (*Client, *[]string) {
	t.Helper()
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		// Drain the body so the query is well-formed JSON (sanity only).
		raw, _ := io.ReadAll(r.Body)
		var probe map[string]any
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		auths = append(auths, auth)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bodyForToken(auth))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, HTTPClient: srv.Client()}
	return c, &auths
}

func TestListPullRequestsEnriched_ParsesAndMaps(t *testing.T) {
	body := `{"data":{"repository":{"pullRequests":{"nodes":[
		{"number":1,"headRefOid":"sha1","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]}},
		{"number":2,"headRefOid":"sha2","mergeable":"CONFLICTING","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE"}}}]}},
		{"number":3,"headRefOid":"sha3","mergeable":"UNKNOWN","commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}},
		{"number":4,"headRefOid":"sha4","mergeable":"MERGEABLE","commits":{"nodes":[]}}
	]}}}}`
	c, _ := graphQLServer(t, func(string) string { return body })

	got, err := c.ListPullRequestsEnriched(context.Background(), "o", "r", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []PullRequestEnriched{
		{Number: 1, HeadSHA: "sha1", Mergeable: "MERGEABLE", CIStatus: "success"},
		{Number: 2, HeadSHA: "sha2", Mergeable: "CONFLICTING", CIStatus: "failure"},
		{Number: 3, HeadSHA: "sha3", Mergeable: "UNKNOWN", CIStatus: ""},
		{Number: 4, HeadSHA: "sha4", Mergeable: "MERGEABLE", CIStatus: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d PRs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PR[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPullRequestEnriched_MergeableBool(t *testing.T) {
	cases := map[string]*bool{
		"MERGEABLE":   boolPtr(true),
		"CONFLICTING": boolPtr(false),
		"UNKNOWN":     nil,
		"":            nil,
	}
	for in, want := range cases {
		got := PullRequestEnriched{Mergeable: in}.MergeableBool()
		switch {
		case want == nil && got != nil:
			t.Errorf("%q: got %v, want nil", in, *got)
		case want != nil && got == nil:
			t.Errorf("%q: got nil, want %v", in, *want)
		case want != nil && got != nil && *want != *got:
			t.Errorf("%q: got %v, want %v", in, *got, *want)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func TestListPullRequestsEnriched_NullRepoNoFallbackIsNotFound(t *testing.T) {
	// repository:null with no FallbackToken → the repo is genuinely
	// invisible to the only token we have → ErrNotFound so the caller
	// drops to the per-PR REST path.
	c, _ := graphQLServer(t, func(string) string {
		return `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`
	})
	_, err := c.ListPullRequestsEnriched(context.Background(), "o", "r", 100)
	if err != ErrNotFound {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestListPullRequestsEnriched_FallsBackToPATWhenAppCannotSeeRepo(t *testing.T) {
	// The cross-namespace case: the App installation token gets
	// repository:null; the configured PAT can see it. The query must retry
	// with the PAT and succeed — otherwise a repo the App doesn't cover
	// never benefits from the batch query.
	c, auths := graphQLServer(t, func(auth string) string {
		if auth == "pat-token" {
			return `{"data":{"repository":{"pullRequests":{"nodes":[
				{"number":7,"headRefOid":"sha7","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"PENDING"}}}]}}
			]}}}}`
		}
		return `{"data":{"repository":null}}`
	})
	c.TokenSource = staticToken("app-token")
	c.FallbackToken = "pat-token"

	got, err := c.ListPullRequestsEnriched(context.Background(), "o", "r", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Number != 7 || got[0].CIStatus != "pending" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if len(*auths) != 2 || (*auths)[0] != "app-token" || (*auths)[1] != "pat-token" {
		t.Fatalf("expected app-token then pat-token, got %v", *auths)
	}
}

func TestListPullRequestsEnriched_RateLimitedSurfaces(t *testing.T) {
	c, _ := graphQLServer(t, func(string) string {
		return `{"data":{"repository":null},"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`
	})
	_, err := c.ListPullRequestsEnriched(context.Background(), "o", "r", 100)
	if err != ErrRateLimited {
		t.Fatalf("got %v, want ErrRateLimited", err)
	}
}

func TestMapRollupState(t *testing.T) {
	cases := map[string]string{
		"SUCCESS":  "success",
		"FAILURE":  "failure",
		"ERROR":    "failure",
		"PENDING":  "pending",
		"EXPECTED": "pending",
		"":         "",
		"WEIRD":    "",
	}
	for in, want := range cases {
		if got := mapRollupState(in); got != want {
			t.Errorf("mapRollupState(%q) = %q, want %q", in, got, want)
		}
	}
}

// Package ship implements Ship Hub Phase 1: GitHub PR sync + deploy
// environment bookkeeping. The service deliberately holds no HTTP /
// websocket plumbing — handlers wire those, this package just talks to
// GitHub and the database.
package ship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
)

// GithubClient is the slice of *gh.Client the service needs. Defining the
// interface here (rather than depending on the concrete type) keeps tests
// from needing an httptest server when they only want to assert on
// upsert behavior.
//
// Phase 3 added the write-side methods. The interface is intentionally a
// subset of *gh.Client's surface — adding a new endpoint requires
// updating both the concrete type and this interface, which is the
// price for the test ergonomics.
type GithubClient interface {
	ListPullRequests(ctx context.Context, owner, repo string, opts gh.ListOptions) ([]gh.PullRequest, error)
	// GetPullRequest fetches a single PR's detail, including
	// merge_commit_sha — the merge-train reconciler needs the true
	// merged SHA (not the head SHA) after a missed webhook.
	GetPullRequest(ctx context.Context, owner, repo string, prNumber int) (*gh.PullRequest, error)
	// GetCIStatus returns the combined CI rollup ("success" |
	// "failure" | "pending" | "") for a SHA — merges legacy commit
	// status + check-runs so the merge-train can gate on Actions
	// (ROA-274). Called per open PR during sync.
	GetCIStatus(ctx context.Context, owner, repo string, sha string) (string, error)
	MergePullRequest(ctx context.Context, owner, repo string, prNumber int, method, sha string) (*gh.MergeResult, error)
	UpdatePullRequestBranch(ctx context.Context, owner, repo string, prNumber int, expectedSHA string) error
	CreatePullRequestComment(ctx context.Context, owner, repo string, prNumber int, body string) (*gh.Comment, error)
	DismissPullRequestReview(ctx context.Context, owner, repo string, prNumber int, reviewID int64, message string) error
	// Phase 6.5 — submit a PR review (Approve / Request changes / Comment).
	// Behavior parity with the chip on GitHub's UI: APPROVE allows an
	// empty body; COMMENT and REQUEST_CHANGES require one. The client
	// enforces this so the handler can return a clean 400.
	SubmitReview(ctx context.Context, owner, repo string, prNumber int, event gh.ReviewEvent, body string) (*gh.Review, error)
	ClosePullRequest(ctx context.Context, owner, repo string, prNumber int) error
	DispatchWorkflow(ctx context.Context, owner, repo, workflowFile, ref string, inputs map[string]string) error
	// Phase 5 — used by the risk classifier. Optional in spirit (the
	// classifier degrades to a title-only heuristic when the call
	// returns an error or empty list), but listed in the interface so
	// the test mocks know they need to provide it.
	ListPullRequestFiles(ctx context.Context, owner, repo string, prNumber int) ([]gh.PullRequestFile, error)
	// PR5a phase 2 — pipeline introspector. Lists workflow files in
	// `.github/workflows/` and reads their YAML contents. Test mocks
	// that don't exercise introspection can return gh.ErrNotFound
	// from both — IntrospectAllWorkspaceProjects treats a 404 as
	// "shape signal absent" rather than an error.
	ListRepoDir(ctx context.Context, owner, repo, path string) ([]gh.RepoContentEntry, error)
	GetRepoFile(ctx context.Context, owner, repo, path string) (string, error)
}

// PRChannelPoster posts a system-style message to a PR's conversation
// channel. Best-effort: callers swallow errors (the chip's primary
// outcome — submitting the review — already succeeded). Wired by the
// handler with a closure that calls into ChannelMessageService.
//
// Phase 6.5 — used by the submit_review action so a member's review
// shows up in the PR's conversation channel without requiring a manual
// re-post. Defined as a function type rather than a fat interface
// because the ship service has exactly one channel-side need today.
type PRChannelPoster func(ctx context.Context, channelID pgtype.UUID, content string) error

// Service is the Ship Hub entry point. Construct one per workspace token
// (so the GithubClient can carry workspace-specific auth) — the periodic
// reconciler in cmd/server constructs a Service per iteration.
type Service struct {
	Q      *db.Queries
	Github GithubClient
	// Now lets tests pin time. nil → time.Now.
	Now func() time.Time
	// PostToPRChannel is optional — when set, the submit_review action
	// best-effort posts a status line to the PR's conversation channel
	// after a successful review submission. nil disables the hook.
	PostToPRChannel PRChannelPoster
}

// SyncResult is the per-call return shape — the handler echoes it back so
// the UI can show "Synced 12 PRs in 2.1s".
type SyncResult struct {
	Repo     string `json:"repo"`
	Upserted int    `json:"upserted"`
	Errors   int    `json:"errors"`
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// SyncProject pulls fresh PR data for one project's attached github_repo
// resources and upserts into the pull_request table. Idempotent: every
// sync produces the same final state when GitHub returns the same payload.
//
// We sync open PRs and the most recently updated closed PRs in two calls,
// then merge — this gives the Kanban a complete "in-flight + recently
// shipped" view without scanning every closed PR ever.
func (s *Service) SyncProject(ctx context.Context, workspaceID, projectID pgtype.UUID) (SyncResult, error) {
	if s.Github == nil {
		return SyncResult{}, errors.New("ship: github client not configured")
	}
	resources, err := s.Q.ListProjectResources(ctx, projectID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("ship: list project resources: %w", err)
	}

	result := SyncResult{}
	for _, res := range resources {
		if res.ResourceType != "github_repo" {
			continue
		}
		repoURL, err := repoURLFromResource(res.ResourceRef)
		if err != nil {
			slog.Warn("ship: skipping github_repo resource with bad ref",
				"resource_id", res.ID, "error", err)
			result.Errors++
			continue
		}
		owner, repo, err := gh.ParseRepoURL(repoURL)
		if err != nil {
			slog.Warn("ship: skipping unparseable repo url",
				"resource_id", res.ID, "url", repoURL, "error", err)
			result.Errors++
			continue
		}
		result.Repo = owner + "/" + repo

		// Open + recently-closed in two calls. We don't paginate beyond
		// page 1 because the per_page=50 default already covers the
		// "active churn" window we care about — projects with >50 open
		// PRs are pathological for a Kanban anyway.
		open, err := s.Github.ListPullRequests(ctx, owner, repo, gh.ListOptions{State: "open"})
		if err != nil {
			slog.Warn("ship: list open PRs failed", "repo", result.Repo, "error", err)
			result.Errors++
			// Continue to closed list — partial success is better than
			// nothing for a manual sync trigger.
		}
		closed, err := s.Github.ListPullRequests(ctx, owner, repo, gh.ListOptions{State: "closed", PerPage: 25})
		if err != nil {
			slog.Warn("ship: list closed PRs failed", "repo", result.Repo, "error", err)
			result.Errors++
		}

		// Per-(owner,repo) cache mapping head_sha → ci_status string. The
		// only purpose is to collapse the per-PR GH fan-out when multiple
		// PRs in this repo share the same head SHA (a release-train branch
		// that's been retargeted, two PRs cherry-picking from the same
		// commit, automation that opens many PRs against one base, etc.).
		//
		// GetCIStatus under the hood makes TWO GH calls per invocation —
		// /commits/{sha}/status + /commits/{sha}/check-runs — so a single
		// shared SHA saves 2N calls for N PRs. On 2026-06-09 the heatmap
		// showed bursts where prellr/multica's commits/{sha}/check-runs
		// path was the hottest entry; this cache flattens the per-sync
		// portion of that pattern. The cache is scoped to one sync of one
		// repo because a SHA's CI status can drift between syncs and we
		// always want a fresh read on the next reconciler tick.
		ciCache := map[string]string{}

		for _, pr := range append(open, closed...) {
			if err := s.upsertPR(ctx, workspaceID, projectID, repoURL, pr); err != nil {
				slog.Warn("ship: upsert PR failed",
					"repo", result.Repo, "pr", pr.Number, "error", err)
				result.Errors++
				continue
			}
			result.Upserted++
			// Live-refresh CI status for OPEN PRs. PR1 of the Ship Hub
			// rebuild took CI status out of upsertPR; this is the
			// counterpart that puts a real refresh back on the sync
			// path — without the clobbering bug that PR1 closed.
			//
			// Webhooks are the primary path for ci_status; this is the
			// periodic + on-demand backup that catches dropped events.
			// Best-effort: a fetch failure logs but doesn't fail the
			// sync (next tick corrects). The Sync Now button hits this
			// same code, so "Sync Now" is finally a real refresh
			// rather than the no-op (or worse, regression) it was
			// before PR1 + PR2 landed.
			//
			// MERGED PRs: also refresh when the cached ci_status is
			// `pending` or empty. Direct-to-prod repos auto-merge as
			// soon as `mergeable=true` even if check-runs are still
			// in-flight — if the final `check_run.completed` webhook
			// lands after the merge OR gets dropped, the cache sticks
			// at "pending" forever because the previous rule only
			// refreshed open PRs (closed-list iteration is bounded at
			// 25, and we skip success/failure rows to keep the GH-call
			// budget tight — bursts of recently-merged PRs would
			// otherwise add up to 25 extra calls per sync per project).
			state := mapPRState(pr)
			shouldRefresh := state == db.PullRequestStateOpen
			if !shouldRefresh && state == db.PullRequestStateMerged {
				if row, err := s.Q.GetPullRequestByNumber(ctx, db.GetPullRequestByNumberParams{
					WorkspaceID: workspaceID,
					RepoUrl:     repoURL,
					PrNumber:    int32(pr.Number),
				}); err == nil {
					cached := strings.ToLower(textValue(row.CiStatus))
					if cached == "" || cached == "pending" {
						shouldRefresh = true
					}
				}
			}
			if shouldRefresh {
				if err := s.refreshPRCIStatus(ctx, workspaceID, repoURL, owner, repo, pr, ciCache); err != nil {
					slog.Warn("ship: refresh PR ci_status failed",
						"repo", result.Repo, "pr", pr.Number, "error", err)
					// Not incrementing result.Errors — the row is
					// still upserted, only the optional refresh slipped.
				}
			}
		}
	}
	return result, nil
}

// refreshPRCIStatus live-fetches the CI rollup for an OPEN PR's head SHA
// and writes the result through the targeted UpdatePullRequestCIStatus
// writer. Mirrors what the check_run webhook does (without needing the
// individual per-check rows) so a missed webhook is repaired by the next
// reconciler tick or any manual Sync Now click.
//
// ciCache (optional, may be nil) is a per-(owner,repo) sha→status map
// owned by the caller — typically scoped to one SyncProject loop body
// so multiple PRs sharing the same head SHA collapse to a single GH
// fetch. Each GetCIStatus invocation makes two GH calls under the hood
// (commits/{sha}/status + commits/{sha}/check-runs), so a single shared
// SHA saves 2N calls for N colliding PRs. Pass nil to opt out and always
// fetch (used by tests and any future caller that hasn't built a cache).
//
// Returns nil on success; on best-effort failures (PR row not found, GH
// fetch fails, write fails) returns a wrapped error the caller should log
// and move on. We never propagate refresh failures up to the SyncProject
// caller because the row is still upserted; only the optional CI rollup
// is missing. A cache hit short-circuits the GH call but still performs
// the per-PR DB write — different PRs need their own ci_status row even
// when the CI verdict is the same.
func (s *Service) refreshPRCIStatus(ctx context.Context, workspaceID pgtype.UUID, repoURL, owner, repo string, pr gh.PullRequest, ciCache map[string]string) error {
	status, err := s.fetchOrCacheCIStatus(ctx, owner, repo, pr.Head.SHA, ciCache)
	if err != nil {
		return fmt.Errorf("fetch ci status: %w", err)
	}
	row, err := s.Q.GetPullRequestByNumber(ctx, db.GetPullRequestByNumberParams{
		WorkspaceID: workspaceID,
		RepoUrl:     repoURL,
		PrNumber:    int32(pr.Number),
	})
	if err != nil {
		return fmt.Errorf("lookup PR row: %w", err)
	}
	if _, err := s.Q.UpdatePullRequestCIStatus(ctx, db.UpdatePullRequestCIStatusParams{
		ID:       row.ID,
		CiStatus: pgtype.Text{String: status, Valid: true},
	}); err != nil {
		return fmt.Errorf("write ci status: %w", err)
	}
	return nil
}

// fetchOrCacheCIStatus returns the CI status string for sha, consulting
// ciCache first when non-nil. A cache miss does the full GetCIStatus
// fetch (two GH calls under the hood — commits/{sha}/status and
// commits/{sha}/check-runs) and writes the result back. Failed fetches
// are NOT cached: a transient error must not pin an empty status for
// the next PR that shares the SHA in the same sync.
//
// Extracted so the dedup behavior can be exercised in unit tests
// without standing up a real db.Queries — the DB-touching half of
// refreshPRCIStatus stays integration-test territory.
func (s *Service) fetchOrCacheCIStatus(ctx context.Context, owner, repo, sha string, ciCache map[string]string) (string, error) {
	if ciCache != nil {
		if status, ok := ciCache[sha]; ok {
			return status, nil
		}
	}
	status, err := s.Github.GetCIStatus(ctx, owner, repo, sha)
	if err != nil {
		return "", err
	}
	if ciCache != nil {
		ciCache[sha] = status
	}
	return status, nil
}

// SyncWorkspace iterates every project with at least one github_repo
// resource and calls SyncProject. Used by the periodic reconciler. Errors
// from individual projects are logged and skipped — one broken repo must
// not stop the rest of the workspace from updating.
func (s *Service) SyncWorkspace(ctx context.Context, workspaceID pgtype.UUID) error {
	// We list projects in the workspace and let SyncProject filter the
	// resources. Could fetch only github_repo project_resources directly
	// instead, but iterating projects keeps the code simple and the
	// workspace-level reconciler runs every 5 minutes — the extra rows
	// are noise compared to the GitHub round-trip.
	projects, err := s.Q.ListProjects(ctx, db.ListProjectsParams{
		WorkspaceID:     workspaceID,
		IncludeArchived: false,
	})
	if err != nil {
		return fmt.Errorf("ship: list workspace projects: %w", err)
	}
	for _, p := range projects {
		if _, err := s.SyncProject(ctx, workspaceID, p.ID); err != nil {
			slog.Warn("ship: sync project failed",
				"workspace_id", uuidString(workspaceID),
				"project_id", uuidString(p.ID),
				"error", err)
			// Keep going — defensive per-project isolation.
			continue
		}
	}
	return nil
}

// upsertPR maps a gh.PullRequest into UpsertPullRequestParams and writes it.
//
// ci_status and review_decision are deliberately NOT written by this path.
// Both columns are owned by webhook-driven writers (UpdatePullRequestCIStatus
// fed by check_run / status events, UpdatePullRequestReviewDecision fed by
// pull_request_review events). Before PR1 of the Ship Hub rebuild, this
// sync path hard-coded both fields to empty string on every call — which
// meant the 5-min reconciler tick blanked webhook-written state every sync
// and the UI showed "CI pending" indefinitely for merged PRs (ROA-203).
// If you need CI status here, fetch it through the webhook path or add a
// targeted UpdatePullRequestCIStatus call AFTER the upsert.
func (s *Service) upsertPR(
	ctx context.Context,
	workspaceID, projectID pgtype.UUID,
	repoURL string,
	pr gh.PullRequest,
) error {
	state := mapPRState(pr)

	labelsJSON, err := json.Marshal(pr.Labels)
	if err != nil {
		// Should never happen (Labels is plain JSON), but if it did the
		// constraint would reject NULL — fall back to an empty array.
		labelsJSON = []byte(`[]`)
	}

	params := db.UpsertPullRequestParams{
		WorkspaceID:     workspaceID,
		ProjectID:       projectID,
		RepoUrl:         repoURL,
		PrNumber:        int32(pr.Number),
		Title:           pr.Title,
		State:           state,
		IsDraft:         pr.Draft,
		AuthorLogin:     pr.User.Login,
		AuthorAvatarUrl: textOrEmpty(pr.User.AvatarURL),
		BaseRef:         pr.Base.Ref,
		HeadRef:         pr.Head.Ref,
		HeadSha:         pr.Head.SHA,
		HtmlUrl:         pr.HTMLURL,
		Body:            textOrEmpty(pr.Body),
		Mergeable:       mapMergeable(pr.Mergeable),
		Additions:       int32(pr.Additions),
		Deletions:       int32(pr.Deletions),
		ChangedFiles:    int32(pr.ChangedFiles),
		Labels:          labelsJSON,
		PrCreatedAt:     pgTime(pr.CreatedAt),
		PrUpdatedAt:     pgTime(pr.UpdatedAt),
		PrMergedAt:      pgTimePtr(pr.MergedAt),
		PrClosedAt:      pgTimePtr(pr.ClosedAt),
		MergeCommitSha:  pr.MergeCommitSHA,
	}
	_, err = s.Q.UpsertPullRequest(ctx, params)
	return err
}

// mapPRState collapses GitHub's two states ("open"/"closed") plus the
// merged_at timestamp into our three-way enum. GitHub's "merged" is just
// "closed AND merged_at IS NOT NULL"; we promote it so the Kanban can
// show a "merged" column without re-deriving it on read.
func mapPRState(pr gh.PullRequest) db.PullRequestState {
	if pr.MergedAt != nil {
		return db.PullRequestStateMerged
	}
	if pr.State == "closed" {
		return db.PullRequestStateClosed
	}
	return db.PullRequestStateOpen
}

// mapMergeable converts GitHub's Boolean (with the *bool null hole) to the
// MERGEABLE/CONFLICTING/UNKNOWN enum we store. nil means GitHub hasn't
// computed it yet — common immediately after an opened-PR webhook.
func mapMergeable(m *bool) pgtype.Text {
	if m == nil {
		return pgtype.Text{String: "UNKNOWN", Valid: true}
	}
	if *m {
		return pgtype.Text{String: "MERGEABLE", Valid: true}
	}
	return pgtype.Text{String: "CONFLICTING", Valid: true}
}

// textOrEmpty returns a Valid=true pgtype.Text even for empty strings.
// Most of these columns are NOT NULL DEFAULT ” on the DB side, so we
// always want to write the actual string.
func textOrEmpty(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

func pgTimePtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// repoURLFromResource pulls the `url` field out of a github_repo
// resource_ref blob. The validator in handler/project_resource.go already
// guarantees the field exists, but this re-derives defensively.
func repoURLFromResource(ref []byte) (string, error) {
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(ref, &payload); err != nil {
		return "", fmt.Errorf("invalid github_repo ref: %w", err)
	}
	if payload.URL == "" {
		return "", errors.New("github_repo ref missing url")
	}
	return payload.URL, nil
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

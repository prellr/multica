package ship

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	gh "github.com/multica-ai/multica/server/pkg/github"
)

// RefreshPullRequest does a live, per-PR fetch from GitHub and writes the
// result back through the targeted UpdatePullRequestStateFromWebhook
// writer.
//
// PR7 of the Ship Hub rebuild — the "mergeable: computing" fix. GitHub
// returns mergeable: null right after a PR opens (it computes the merge
// asynchronously); Ship maps that to mergeable = "UNKNOWN" and never
// re-polls, so a PR card shows "computing" forever until a manual sync.
// This method is the endpoint behind the frontend's useMergeablePoll
// hook: it re-reads the single PR and persists whatever GitHub now says.
//
// It deliberately writes through UpdatePullRequestStateFromWebhook rather
// than upsertPR: that writer touches only the mutable PR fields (title,
// state, draft, refs, head_sha, body, mergeable, timestamps) and leaves
// ci_status / review_decision / diff stats alone — those columns are
// owned by the webhook-driven targeted writers, and a live PR fetch
// doesn't carry a reliable check rollup anyway.
func (s *Service) RefreshPullRequest(ctx context.Context, prID pgtype.UUID) (db.PullRequest, error) {
	row, err := s.Q.GetPullRequest(ctx, prID)
	if err != nil {
		return db.PullRequest{}, fmt.Errorf("get pull request: %w", err)
	}

	owner, repo, err := gh.ParseRepoURL(row.RepoUrl)
	if err != nil {
		return db.PullRequest{}, fmt.Errorf("parse repo url: %w", err)
	}

	pr, err := s.Github.GetPullRequest(ctx, owner, repo, int(row.PrNumber))
	if err != nil {
		return db.PullRequest{}, fmt.Errorf("fetch pull request from github: %w", err)
	}

	updated, err := s.Q.UpdatePullRequestStateFromWebhook(ctx, db.UpdatePullRequestStateFromWebhookParams{
		ID:          row.ID,
		Title:       pr.Title,
		State:       mapPRState(*pr),
		IsDraft:     pr.Draft,
		BaseRef:     pr.Base.Ref,
		HeadRef:     pr.Head.Ref,
		HeadSha:     pr.Head.SHA,
		Body:        textOrEmpty(pr.Body),
		Mergeable:   mapMergeable(pr.Mergeable),
		PrUpdatedAt: pgTime(pr.UpdatedAt),
		PrMergedAt:  pgTimePtr(pr.MergedAt),
		PrClosedAt:  pgTimePtr(pr.ClosedAt),
	})
	if err != nil {
		return db.PullRequest{}, fmt.Errorf("write refreshed pull request: %w", err)
	}
	return updated, nil
}

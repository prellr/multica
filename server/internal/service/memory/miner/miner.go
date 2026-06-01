package miner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// MineDecisions is the public entrypoint for the decision-miner pass.
// It scans issues (optionally scoped to a project) in the given
// workspace, runs the heuristic detector against each comment, and
// either reports the matches (DryRun=true) or writes them as
// kind="decision" memory artifacts anchored to the source issue.
//
// Idempotency contract: re-running against the same workspace must
// NOT produce duplicate artifacts. We achieve this by reading
// existing mined artifacts (tag="mined") up-front, parsing their
// metadata.source_comment_id, and skipping any candidate whose
// source comment is already represented. Re-runs of an expanded
// detector (new patterns) DO produce new artifacts because the
// detectorVersion is part of the metadata — operators can prune the
// older rows by querying `metadata.detector_version != current`.
//
// Tag convention — every artifact created here carries:
//   - "mined"               — the verification queue selector
//   - "decision-candidate"  — the human-readable status
//
// Anchor — every artifact anchors to its source issue. This is what
// makes the substrate compound: future agents working on the issue
// see the mined decision via the by-anchor injection path.
func MineDecisions(ctx context.Context, q *db.Queries, opts Options) (*Result, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	opts.applyDefaults()

	res := &Result{Project: opts.ProjectLabel}

	// 1. Load existing mined comment IDs for dedup. Hard-cap at
	//    seenScanLimit because a workspace with that many mined
	//    artifacts is well past the bootstrap phase and the detector
	//    pattern is the bottleneck, not idempotency.
	seen, err := loadSeenComments(ctx, q, opts.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("load seen comments: %w", err)
	}

	// 2. Scan issues in scope.
	issues, err := q.ListIssues(ctx, db.ListIssuesParams{
		WorkspaceID: opts.WorkspaceID,
		Limit:       int32(opts.Limit),
		Offset:      0,
		Kind:        "issue",
		ProjectID:   opts.ProjectID,
		// Other filter fields are left zero — sqlc generated their
		// SQL with `(arg IS NULL OR ...)` patterns, so zero values
		// disable them. We rely on the default sort order.
	})
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	res.IssuesScanned = len(issues)

	// 3. For each issue, scan the description (which often contains the
	//    canonical "**Decision:** ..." section in design-doc-style
	//    workspaces) AND the comments.
	for _, iss := range issues {
		// 3a. Description scan. We synthesize a deterministic
		//     "comment-like" ID from the issue ID for dedup keying —
		//     prefixed "issue-desc:" so it can never collide with a
		//     real comment UUID.
		if iss.Description.Valid && iss.Description.String != "" {
			res.DescriptionsScanned++
			descKey := "issue-desc:" + uuidToString(iss.ID)
			if !seen[descKey] {
				if det := detectDecision(iss.Description.String); det != nil {
					res.Matches = append(res.Matches, Match{
						Source:          SourceDescription,
						IssueID:         iss.ID,
						IssueTitle:      iss.Title,
						IssueIdentifier: issueIdentifier(iss),
						// CommentID stays zero — descriptions have no
						// owning comment. Dedup uses descKey via the
						// writer's metadata.source_comment_id field.
						SourceKey:     descKey,
						CommentAuthor: "issue:creator",
						CommentDate:   iss.CreatedAt.Time,
						Snippet:       det.Snippet,
						Phrase:        det.Phrase,
						Reason:        det.Reason,
					})
				}
			}
		}

		// 3b. Comments scan.
		comments, err := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
			IssueID:     iss.ID,
			WorkspaceID: opts.WorkspaceID,
			Limit:       commentScanLimit,
		})
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("comments for %s: %w", iss.Title, err))
			continue
		}
		for _, c := range comments {
			res.CommentsScanned++
			if c.Type == "system" {
				// Auto-emitted state-change comments aren't decisions
				// regardless of what their templated text matches.
				continue
			}
			if !opts.Since.IsZero() && c.CreatedAt.Valid && c.CreatedAt.Time.Before(opts.Since) {
				continue
			}
			commentIDStr := uuidToString(c.ID)
			if seen[commentIDStr] {
				continue
			}
			det := detectDecision(c.Content)
			if det == nil {
				continue
			}
			res.Matches = append(res.Matches, Match{
				Source:          SourceComment,
				IssueID:         iss.ID,
				IssueTitle:      iss.Title,
				IssueIdentifier: issueIdentifier(iss),
				CommentID:       c.ID,
				SourceKey:       commentIDStr,
				CommentAuthor:   formatAuthor(c.AuthorType, c.AuthorID),
				CommentDate:     c.CreatedAt.Time,
				Snippet:         det.Snippet,
				Phrase:          det.Phrase,
				Reason:          det.Reason,
			})
		}
	}

	// 4. Write artifacts unless dry-run.
	if opts.DryRun {
		return res, nil
	}
	for _, m := range res.Matches {
		id, err := writeArtifact(ctx, q, opts, m)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("write artifact for %s: %w", m.IssueIdentifier, err))
			continue
		}
		res.Created = append(res.Created, id)
	}
	return res, nil
}

// writeArtifact persists one decision-candidate as a memory artifact.
// Title prefixed with "Decision (proposed):" so it reads correctly in
// the list view BEFORE a human verifies it — verified entries get
// renamed manually as part of the verify workflow.
func writeArtifact(ctx context.Context, q *db.Queries, opts Options, m Match) (string, error) {
	title := truncate("Decision (proposed): "+m.IssueTitle, 200)
	content := buildContent(m)
	// source_comment_id carries the dedup key — for descriptions it's
	// the synthetic "issue-desc:<uuid>", for comments it's the comment
	// UUID. loadSeenComments matches on this field for both.
	metaJSON, err := json.Marshal(map[string]any{
		"source_issue_id":   uuidToString(m.IssueID),
		"source_issue":      m.IssueIdentifier,
		"source":            string(m.Source),
		"source_comment_id": m.SourceKey,
		"source_author":     m.CommentAuthor,
		"matched_phrase":    m.Phrase,
		"reason":            m.Reason,
		"detector_version":  detectorVersion,
		"mined_at":          time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}
	row, err := q.CreateMemoryArtifact(ctx, db.CreateMemoryArtifactParams{
		WorkspaceID: opts.WorkspaceID,
		Kind:        "decision",
		Title:       title,
		Content:     content,
		AuthorType:  opts.AuthorType,
		AuthorID:    opts.AuthorID,
		Tags:        []string{"mined", "decision-candidate"},
		Metadata:    metaJSON,
		// AlwaysInjectAtRuntime intentionally NOT set — proposed
		// decisions must be verified before they earn the
		// always-inject privilege. The verify workflow flips it.
		AnchorType: pgtype.Text{String: "issue", Valid: true},
		AnchorID:   m.IssueID,
	})
	if err != nil {
		return "", err
	}
	return uuidToString(row.ID), nil
}

// buildContent formats the artifact body. Quoted excerpt + metadata
// footer so a human reading the artifact in the UI sees the
// provenance without clicking through.
func buildContent(m Match) string {
	dateStr := "(unknown date)"
	if !m.CommentDate.IsZero() {
		dateStr = m.CommentDate.Format("2006-01-02")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Mined from %s** — comment by %s on %s\n\n", m.IssueIdentifier, m.CommentAuthor, dateStr)
	fmt.Fprintf(&b, "> %s\n\n", m.Snippet)
	fmt.Fprintf(&b, "_Detected by: `%s` (matched: \"%s\")_\n", m.Reason, m.Phrase)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "_This is a PROPOSED decision artifact. Verify, edit, or archive it._")
	return b.String()
}

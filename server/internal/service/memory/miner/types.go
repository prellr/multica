package miner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Tunables. issueScanLimit caps how many issues a single mining pass
// touches — set high enough to cover real projects (RoastConsole has
// ~150 issues; Multica has more) but low enough that a runaway run
// can't drag the DB. commentScanLimit reflects the comments-per-issue
// p99 in production. seenScanLimit is the dedup pre-load: once a
// workspace has more mined artifacts than this the detector is
// already saturated and the dedup is mostly noise.
const (
	defaultIssueScanLimit = 500
	commentScanLimit      = 500
	seenScanLimit         = 5000
)

// Options drives a mining pass. WorkspaceID + AuthorType + AuthorID
// are required; everything else has a working default.
type Options struct {
	WorkspaceID pgtype.UUID
	// ProjectID scopes the scan; zero (Valid=false) means
	// workspace-wide. Set this in v1 — workspace-wide scans across
	// thousands of issues are best left until we have the per-project
	// experience to predict the result.
	ProjectID pgtype.UUID
	// ProjectLabel is a human-readable hint surfaced in Result —
	// purely for the CLI/HTTP report. Optional.
	ProjectLabel string
	// Since restricts to comments created at-or-after this time. Zero
	// = no time floor. Useful for incremental re-runs.
	Since time.Time
	// Limit caps the number of issues scanned in one pass. Default
	// defaultIssueScanLimit.
	Limit int
	// DryRun reports what WOULD be created without writing.
	DryRun bool
	// AuthorType / AuthorID stamp the created artifact's author. For
	// CLI-driven runs this is the calling member; for scheduled runs
	// we'll create a "memory-miner" agent and pass its identity.
	AuthorType string
	AuthorID   pgtype.UUID
}

// validate returns a clear error before any DB work happens.
func (o *Options) validate() error {
	if !o.WorkspaceID.Valid {
		return errors.New("WorkspaceID is required")
	}
	if o.AuthorType != "member" && o.AuthorType != "agent" {
		return fmt.Errorf("AuthorType must be 'member' or 'agent', got %q", o.AuthorType)
	}
	if !o.AuthorID.Valid {
		return errors.New("AuthorID is required")
	}
	return nil
}

func (o *Options) applyDefaults() {
	if o.Limit <= 0 {
		o.Limit = defaultIssueScanLimit
	}
}

// Source enumerates where a Match came from. The 2026-05-28 prod
// preview against RoastConsole Cloud showed ~36% of high-quality
// candidates live in descriptions (the canonical "**Decision:**"
// label in design-doc-style issues), not comments — so the source
// is part of the artifact's provenance.
type Source string

const (
	SourceDescription Source = "description"
	SourceComment     Source = "comment"
)

// Match is one detected decision-candidate, used both for dry-run
// reports and as input to the artifact writer.
type Match struct {
	Source          Source
	IssueID         pgtype.UUID
	IssueTitle      string
	IssueIdentifier string      // e.g. "ROA-427" if we can derive it; empty otherwise
	CommentID       pgtype.UUID // zero for description-source matches
	// SourceKey is the dedup key: comment UUID for comment sources,
	// "issue-desc:<uuid>" for description sources. Always populated.
	SourceKey     string
	CommentAuthor string // "member:<short>" / "agent:<short>" / "issue:creator"
	CommentDate   time.Time
	Snippet       string
	Phrase        string
	Reason        string
}

// Result is the return type for MineDecisions. The fields are shaped
// for both the CLI output and the JSON HTTP response.
type Result struct {
	Project             string   `json:"project,omitempty"`
	IssuesScanned       int      `json:"issues_scanned"`
	DescriptionsScanned int      `json:"descriptions_scanned"`
	CommentsScanned     int      `json:"comments_scanned"`
	Matches             []Match  `json:"matches"`
	Created             []string `json:"created"` // memory artifact IDs
	Errors              []error  `json:"-"`       // surfaced separately by caller
}

// loadSeenComments pulls existing mined artifacts and returns a set
// of their source_comment_id values. Used by MineDecisions to skip
// re-proposing the same comment across runs.
func loadSeenComments(ctx context.Context, q *db.Queries, wsID pgtype.UUID) (map[string]bool, error) {
	seen := map[string]bool{}
	// Use the tag filter (#172) to limit the scan to mined rows only.
	rows, err := q.ListMemoryArtifacts(ctx, db.ListMemoryArtifactsParams{
		WorkspaceID:     wsID,
		Tags:            []string{"mined"},
		IncludeArchived: false,
		// IncludeSystem irrelevant — kind=decision is a human kind.
		Limit:  int32(seenScanLimit),
		Offset: 0,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		var meta map[string]any
		if err := json.Unmarshal(r.Metadata, &meta); err != nil {
			continue // unparseable metadata = treat as not-seen; conservative
		}
		if id, ok := meta["source_comment_id"].(string); ok && id != "" {
			seen[id] = true
		}
	}
	return seen, nil
}

// uuidToString renders a pgtype.UUID as the canonical string form
// (with hyphens). pgtype's String() method returns the
// implementation-defined form, which has changed across versions —
// this wrapper is the stable serializer used everywhere we hand a
// UUID back to a caller.
func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// issueIdentifier returns a human-readable identifier for an issue.
// The Issue struct exposes `Number` (workspace-scoped int) but not
// the workspace prefix; for the artifact body we use "Issue #<n>" as
// a graceful fallback when the caller doesn't supply the prefix.
// The CLI/handler can post-process to fill in "ROA-<n>" if it has
// the workspace context loaded.
func issueIdentifier(iss db.ListIssuesRow) string {
	if iss.Number.Valid {
		return fmt.Sprintf("#%d", iss.Number.Int32)
	}
	return uuidToString(iss.ID)
}

// formatAuthor produces a short "<type>:<8 hex>" identifier suitable
// for embedding in the artifact body. Real names would require a
// member/agent lookup; the v1 artifact carries the IDs and lets the
// UI resolve names at display time.
func formatAuthor(authorType string, authorID pgtype.UUID) string {
	id := uuidToString(authorID)
	if len(id) >= 8 {
		id = id[:8]
	}
	return authorType + ":" + id
}

// truncate clips a string to n runes, appending "…" if cut. Used for
// the artifact title (capped at the DB's 500-rune limit but we stay
// well under for readability).
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

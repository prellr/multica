// ROA-274 — pure unit tests for releaseEligibilityReason's CI gate.
//
// The function is pure: given a db.PullRequest it returns (reason, ok)
// with no DB, clock, or network. This file pins the CI-status branch
// in isolation so a regression in the merge-train gate points at
// exactly one function. The end-to-end "sync populates ci_status →
// gate blocks" path is covered by the handler-package DB tests.

package ship

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestReleaseEligibilityReason_CIGate(t *testing.T) {
	openPR := func(ci string) db.PullRequest {
		return db.PullRequest{
			State:     db.PullRequestStateOpen,
			IsDraft:   false,
			Mergeable: pgtype.Text{String: "MERGEABLE", Valid: true},
			CiStatus:  pgtype.Text{String: ci, Valid: true},
		}
	}

	cases := []struct {
		name     string
		pr       db.PullRequest
		wantOK   bool
		wantsHas string // substring expected in the reason when blocked
	}{
		{
			name:     "ci failure blocks",
			pr:       openPR("failure"),
			wantOK:   false,
			wantsHas: "CI failed",
		},
		{
			name:   "ci pending is eligible (soft warning only)",
			pr:     openPR("pending"),
			wantOK: true,
		},
		{
			name:   "ci success is eligible",
			pr:     openPR("success"),
			wantOK: true,
		},
		{
			// CI-less repo (RoastConsole-style): empty ci_status must
			// stay mergeable, otherwise we false-block repos that have
			// no CI configured at all.
			name:   "empty ci (CI-less repo) is eligible",
			pr:     openPR(""),
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := releaseEligibilityReason(tc.pr)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v (reason=%q)", ok, tc.wantOK, reason)
			}
			if !tc.wantOK && tc.wantsHas != "" && reason != tc.wantsHas {
				t.Fatalf("reason: got %q, want %q", reason, tc.wantsHas)
			}
		})
	}
}

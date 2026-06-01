package miner

import (
	"strings"
	"testing"
)

// Detector tests — purely string-in, decision-or-nil out. These pin
// down what the precision-first heuristic does and doesn't catch so
// future tweaks ("loosen this regex a bit") show up as concrete
// new/changed test rows rather than silent recall regressions.

func TestDetectDecision_Matches(t *testing.T) {
	cases := []struct {
		name           string
		text           string
		wantReason     string
		wantSnippetHas string // a substring expected to be in the snippet
	}{
		{
			name:           "explicit label - Decision:",
			text:           "After all that back-and-forth: Decision: we ship behind a flag and iterate.",
			wantReason:     "explicit-label",
			wantSnippetHas: "Decision:",
		},
		{
			name:           "explicit label - Verdict:",
			text:           "Looked at all three options. Verdict: option B for the migration.",
			wantReason:     "explicit-label",
			wantSnippetHas: "Verdict:",
		},
		{
			name:           "decided to ship",
			text:           "We decided to ship the simpler path and revisit caching in Q3 if needed.",
			wantReason:     "decided-to",
			wantSnippetHas: "decided to",
		},
		{
			name:           "decided against rebuild",
			text:           "After scoping it out we decided against a full rebuild — too risky this cycle.",
			wantReason:     "decided-to",
			wantSnippetHas: "decided against",
		},
		{
			name:           "going with X",
			text:           "Looked at Postgres vs Mongo for this. Going with Postgres — schema discipline matters here.",
			wantReason:     "going-with",
			wantSnippetHas: "Going with",
		},
		{
			name:           "we'll go with",
			text:           "Both options work, but we'll go with the cron-based approach for simplicity.",
			wantReason:     "going-with",
			wantSnippetHas: "we'll go with",
		},
		{
			name:           "ruled out Redis",
			text:           "Pricing on Redis cluster was prohibitive, so we ruled out the managed Redis path entirely.",
			wantReason:     "ruled-out",
			wantSnippetHas: "ruled out",
		},
		{
			name:           "the call is X",
			text:           "Talked it through with Ryan — the call is to bundle this into the 0.3 release.",
			wantReason:     "the-call-is",
			wantSnippetHas: "the call is",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := detectDecision(tc.text)
			if m == nil {
				t.Fatalf("expected match, got nil for: %q", tc.text)
			}
			if m.Reason != tc.wantReason {
				t.Fatalf("reason: want %q, got %q", tc.wantReason, m.Reason)
			}
			if tc.wantSnippetHas != "" && !containsFold(m.Snippet, tc.wantSnippetHas) {
				t.Fatalf("snippet should contain %q; got %q", tc.wantSnippetHas, m.Snippet)
			}
		})
	}
}

func TestDetectDecision_NoMatch(t *testing.T) {
	// Precision-first: every one of these is the kind of text that a
	// loose regex would catch but isn't actually a decision. If any
	// of these starts matching, the verification queue will fill up
	// with noise — fix the regex or drop the bad pattern.
	cases := []struct {
		name string
		text string
	}{
		{"too short", "ok"},
		{"empty after trim", "   \n  "},
		{"discussion - we will deploy", "Heads up, we will deploy this on Friday afternoon."},
		{"opinion - I think", "I think we should consider Postgres for this — happy to discuss."},
		{"question", "Should we be going with the bundled approach or two services?"},
		{"general going-to", "Going to the docs to look up the right config flag."},
		{"unrelated call", "Going to call the customer back tomorrow morning."},
		{"reaction comment", "+1 lgtm 👍"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if m := detectDecision(tc.text); m != nil {
				t.Fatalf("expected NO match, got reason=%q snippet=%q for: %q", m.Reason, m.Snippet, tc.text)
			}
		})
	}
}

func TestExtractSnippet_PaddingAndClamp(t *testing.T) {
	text := "Beginning of the comment. Decision: option B. Trailing context after the decision."
	// Match the literal "Decision:" (case-sensitive find in this fixture)
	start := indexOf(text, "Decision:")
	end := start + len("Decision:")
	got := extractSnippet(text, start, end)
	if !containsFold(got, "Decision:") {
		t.Fatalf("snippet should contain matched phrase; got %q", got)
	}
	// The text is short enough that neither ellipsis should appear
	// (no truncation occurred on either side).
	if strings.HasPrefix(got, "…") || strings.HasSuffix(got, "…") {
		t.Fatalf("short text should not produce ellipses; got %q", got)
	}
}

func TestExtractSnippet_Truncates(t *testing.T) {
	// Build a string wide enough on both sides that the snippet
	// must truncate and both ellipses appear.
	pad := ""
	for i := 0; i < snippetRadius*3; i++ {
		pad += "x "
	}
	text := pad + " Decision: option B. " + pad
	start := indexOf(text, "Decision:")
	end := start + len("Decision:")
	got := extractSnippet(text, start, end)
	if !strings.HasPrefix(got, "…") {
		t.Fatalf("expected leading ellipsis on truncated snippet; got prefix %q", got[:min(len(got), 8)])
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected trailing ellipsis on truncated snippet; got tail %q", got[max(0, len(got)-8):])
	}
}

// Small helpers kept inline to avoid a strings/utf8 import for a 2-line
// containsFold (case-insensitive) and an indexOf wrapper.
func containsFold(haystack, needle string) bool {
	return indexOfFold(haystack, needle) >= 0
}

func indexOfFold(haystack, needle string) int {
	hl, nl := len(haystack), len(needle)
	if nl == 0 {
		return 0
	}
	for i := 0; i+nl <= hl; i++ {
		eq := true
		for j := 0; j < nl; j++ {
			a, b := haystack[i+j], needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				eq = false
				break
			}
		}
		if eq {
			return i
		}
	}
	return -1
}

func indexOf(haystack, needle string) int {
	hl, nl := len(haystack), len(needle)
	for i := 0; i+nl <= hl; i++ {
		if haystack[i:i+nl] == needle {
			return i
		}
	}
	return -1
}

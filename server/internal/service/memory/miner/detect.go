// Package miner extracts memory-artifact candidates from existing
// workspace content (issues, comments, ...) so the memory substrate
// accumulates signal from work that happened before it existed.
//
// Detector design — high-precision over high-recall. The output is a
// candidate queue that a human verifies, so a false positive costs a
// click; a false negative costs a missed decision the substrate will
// never carry forward. We err toward FEWER, BETTER matches.
//
// The patterns below were chosen by stress-testing common engineering
// chat against false positives:
//
//   - "we will deploy on Friday" — common project talk, NOT a decision.
//     Excluded.
//   - "I think we should..." — opinion, not a verdict. Excluded.
//   - "decision: we're using Postgres" — explicit label, strong signal.
//     Included.
//   - "going with PG over MySQL" — common decision shorthand. Included.
//   - "ruled out Redis cluster" — explicit rejection. Included.
//
// If a single comment matches more than one pattern we still emit ONE
// Match (the first pattern wins as `Reason`). Re-running the miner on
// a workspace MUST be idempotent — caller-side dedup keys on the source
// comment ID stored in the artifact's metadata.
package miner

import (
	"regexp"
	"strings"
)

// detectorVersion is stamped into each artifact's metadata so a future
// re-run with improved patterns can identify rows that were created by
// an earlier (worse) version and consider re-mining them. Bump whenever
// `decisionPatterns` changes substantively.
const detectorVersion = "decisions/v1"

// snippetRadius is the half-width (in chars) of the excerpt extracted
// around a matched phrase. 120 is wide enough to read context without
// dragging the whole comment into the artifact body.
const snippetRadius = 120

// minCommentLen filters out reaction-style comments ("ok", "+1", "lgtm",
// "👍") that can't carry a decision regardless of what they pattern-match.
// 30 chars is short enough not to lose substantive one-liners.
const minCommentLen = 30

// decisionMatch is what the detector returns per matching comment.
// Snippet is already trimmed and ellipsis-padded for display.
type decisionMatch struct {
	Snippet string // excerpt around the matched phrase, ±snippetRadius chars
	Phrase  string // the literal phrase that matched (lowercase)
	Reason  string // short tag describing which pattern fired
}

// decisionPatterns is the precision-first list. Each entry pairs a
// regex with a short reason tag that flows through to the Match struct
// and into the artifact's metadata, so a human triaging the queue can
// sort/filter by detection mechanism.
//
// Patterns are ordered roughly by precision (highest first). The first
// match wins for `Reason`; subsequent patterns on the same comment are
// still consulted only to know that we have a match.
var decisionPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	// Explicit labelling — "Decision:" / "Verdict:" / "Final call:" /
	// "The call:" / "TL;DR:". Word-boundary + label noun + colon.
	// Colon (not period) is required: this is the *label* form, not
	// the end-of-sentence "we got the decision." form. Works inline
	// ("After all that: Decision: option B") and at line-start.
	{
		re:     regexp.MustCompile(`(?i)\b(?:decision|verdict|final\s+call|the\s+call|tl;?dr)\s*:`),
		reason: "explicit-label",
	},
	// "decided to / against / not to / on" — past-tense commitment.
	// Catches "we decided to ship next sprint" and "decided against
	// rebuilding it." High precision because the verb tense itself
	// implies a closed conversation.
	{
		re:     regexp.MustCompile(`(?i)\bdecided\s+(?:to|against|not\s+to|on)\b`),
		reason: "decided-to",
	},
	// "going with X" / "we'll go with X" / "let's go with X" / sentence-
	// start "Going with X" — the dominant idiom for picking between
	// options. Two shapes: (a) preceded by a pronoun/let's, anywhere;
	// (b) start of sentence or paragraph, no pronoun needed. "with" is
	// required to disambiguate from "going to the gym" / "going to the
	// docs."
	{
		re:     regexp.MustCompile(`(?im)(?:\b(?:we'?re|we\s+are|i'?m|let'?s|we'?ll|i'?ll|gonna|going\s+to)\s+(?:going|gonna|go)?\s*with\b|(?:^|[.!?]\s+)(?:going|gonna)\s+with\b)`),
		reason: "going-with",
	},
	// "ruled out X" — explicit rejection. Pairs naturally with
	// "going-with" — a complete decision often has both halves.
	{
		re:     regexp.MustCompile(`(?i)\bruled\s+out\b`),
		reason: "ruled-out",
	},
	// "the call is X" / "my call is X" — "call" as noun, the verdict
	// idiom. Tight regex to avoid "call me" / "call the API".
	{
		re:     regexp.MustCompile(`(?i)\b(?:the|my|our)\s+call\s+(?:is|was|here\s+is)\b`),
		reason: "the-call-is",
	},
}

// detectDecision returns a match if the comment text triggers any of
// the precision-first patterns, or nil if not. Returns nil for
// comments too short to be substantive.
func detectDecision(text string) *decisionMatch {
	if len(strings.TrimSpace(text)) < minCommentLen {
		return nil
	}
	for _, p := range decisionPatterns {
		loc := p.re.FindStringIndex(text)
		if loc == nil {
			continue
		}
		return &decisionMatch{
			Snippet: extractSnippet(text, loc[0], loc[1]),
			Phrase:  strings.ToLower(text[loc[0]:loc[1]]),
			Reason:  p.reason,
		}
	}
	return nil
}

// extractSnippet returns text[start:end] with ±snippetRadius chars of
// surrounding context. Boundaries are clamped to the string and
// padded with "…" when truncated, so the result is always a clean
// human-readable excerpt.
func extractSnippet(text string, matchStart, matchEnd int) string {
	lo := matchStart - snippetRadius
	hi := matchEnd + snippetRadius
	prefix, suffix := "", ""
	if lo < 0 {
		lo = 0
	} else {
		prefix = "…"
	}
	if hi > len(text) {
		hi = len(text)
	} else {
		suffix = "…"
	}
	// Snap to the nearest whitespace on either side so we don't slice
	// mid-word. Skip the snap if it would shrink the snippet by more
	// than half (we'd rather show a mid-word break than nothing).
	if lo > 0 {
		if i := strings.IndexAny(text[lo:matchStart], " \n\t"); i >= 0 && i < snippetRadius/2 {
			lo += i + 1
		}
	}
	if hi < len(text) {
		if i := strings.LastIndexAny(text[matchEnd:hi], " \n\t"); i >= 0 && i > snippetRadius/2 {
			hi = matchEnd + i
		}
	}
	return prefix + strings.TrimSpace(text[lo:hi]) + suffix
}

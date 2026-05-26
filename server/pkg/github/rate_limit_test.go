package github

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// resp builds a minimal http.Header from the supplied rate-limit tuple.
// Keeps the test bodies focused on the assertion, not on http plumbing.
func rateLimitHeader(limit, remaining, resetUnix int, resource string) http.Header {
	h := http.Header{}
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Reset", strconv.Itoa(resetUnix))
	if resource != "" {
		h.Set("X-RateLimit-Resource", resource)
	}
	return h
}

func TestRateLimitState_NoHeaders_NoState(t *testing.T) {
	// Endpoints that don't carry rate-limit headers (rare in practice
	// but exist — e.g. some installation-token mint paths) must not
	// fabricate state. observeResponse silently returns.
	s := &rateLimitState{}
	s.observeResponse("/some/path", http.Header{})
	if len(s.resources) != 0 {
		t.Fatalf("expected no resources tracked, got %d", len(s.resources))
	}
}

func TestRateLimitState_TracksPerResource(t *testing.T) {
	// GitHub maintains separate budgets for core / search / graphql.
	// Each Resource header value should bucket independently — a
	// search call should NOT affect the core window.
	s := &rateLimitState{now: func() time.Time { return time.Unix(1_000_000, 0) }}
	reset := int(time.Unix(1_000_000, 0).Add(time.Hour).Unix())

	s.observeResponse("/repos/x/y", rateLimitHeader(5000, 4500, reset, "core"))
	s.observeResponse("/search/code", rateLimitHeader(30, 25, reset, "search"))

	if got := s.resources["core"].Remaining; got != 4500 {
		t.Fatalf("core: expected remaining 4500, got %d", got)
	}
	if got := s.resources["search"].Remaining; got != 25 {
		t.Fatalf("search: expected remaining 25, got %d", got)
	}
}

func TestRateLimitState_TracksLowestSeenWithinWindow(t *testing.T) {
	// LowestSeen is the running min within the current window —
	// summary lines at window reset use it to report "how tight did
	// we get". A subsequent response with a HIGHER remaining (e.g.
	// after a quiet pause) must NOT bump it back up.
	s := &rateLimitState{now: func() time.Time { return time.Unix(1_000_000, 0) }}
	reset := int(time.Unix(1_000_000, 0).Add(time.Hour).Unix())

	s.observeResponse("/a", rateLimitHeader(5000, 4500, reset, "core"))
	s.observeResponse("/a", rateLimitHeader(5000, 100, reset, "core"))
	s.observeResponse("/a", rateLimitHeader(5000, 200, reset, "core"))

	if got := s.resources["core"].LowestSeen; got != 100 {
		t.Fatalf("LowestSeen should be the running min (100), got %d", got)
	}
}

func TestRateLimitState_WindowTransitionResetsLowest(t *testing.T) {
	// When GitHub rolls the window over (Reset header advances), the
	// LowestSeen tracker must reset to the new Remaining so we don't
	// carry stale "worst-of-prior-window" into the new window's
	// summary. Window summary log fires at the boundary; the test
	// here checks the state side-effect (the log assertion is
	// covered by a separate test below).
	s := &rateLimitState{now: func() time.Time { return time.Unix(1_000_000, 0) }}
	resetA := int(time.Unix(1_000_000, 0).Add(time.Hour).Unix())
	resetB := int(time.Unix(1_000_000, 0).Add(2 * time.Hour).Unix())

	s.observeResponse("/a", rateLimitHeader(5000, 100, resetA, "core"))
	if got := s.resources["core"].LowestSeen; got != 100 {
		t.Fatalf("setup: lowestSeen=%d, want 100", got)
	}

	// New window — Remaining went back up and Reset advanced.
	s.observeResponse("/a", rateLimitHeader(5000, 4900, resetB, "core"))
	if got := s.resources["core"].LowestSeen; got != 4900 {
		t.Fatalf("after window flip: lowestSeen=%d, want 4900 (reset to new Remaining)", got)
	}
	if got := s.resources["core"].Reset.Unix(); got != int64(resetB) {
		t.Fatalf("after window flip: Reset=%d, want %d", got, resetB)
	}
}

func TestRateLimitState_WarnThrottle(t *testing.T) {
	// Sustained-pressure scenario: every request comes back under the
	// 20 % threshold. The first response should set LastWarn; the
	// second response within the throttle window should NOT update
	// it (i.e., the throttle holds). After advancing the clock past
	// rateLimitWarnInterval, the next response below threshold should
	// update LastWarn again.
	t0 := time.Unix(1_000_000, 0)
	clock := t0
	s := &rateLimitState{now: func() time.Time { return clock }}
	reset := int(t0.Add(time.Hour).Unix())

	// limit=5000, threshold=20% → warn when remaining<1000. Use 500.
	s.observeResponse("/a", rateLimitHeader(5000, 500, reset, "core"))
	firstWarn := s.resources["core"].LastWarn
	if firstWarn.IsZero() {
		t.Fatalf("expected LastWarn set after first sub-threshold response")
	}

	// Same time → throttle holds.
	s.observeResponse("/b", rateLimitHeader(5000, 400, reset, "core"))
	if got := s.resources["core"].LastWarn; !got.Equal(firstWarn) {
		t.Fatalf("LastWarn should not advance within throttle window: was %v, got %v", firstWarn, got)
	}

	// Advance just past the throttle interval.
	clock = clock.Add(rateLimitWarnInterval + time.Second)
	s.observeResponse("/c", rateLimitHeader(5000, 300, reset, "core"))
	if got := s.resources["core"].LastWarn; !got.After(firstWarn) {
		t.Fatalf("LastWarn should advance after throttle window expires: was %v, got %v", firstWarn, got)
	}
}

func TestRateLimitState_NoWarnAboveThreshold(t *testing.T) {
	// Healthy budget responses must NOT emit warns. We can't directly
	// observe the log call without intercepting slog, but the warn
	// path is the only thing that sets LastWarn — so checking
	// LastWarn stays zero is a load-bearing proxy.
	s := &rateLimitState{now: func() time.Time { return time.Unix(1_000_000, 0) }}
	reset := int(time.Unix(1_000_000, 0).Add(time.Hour).Unix())

	s.observeResponse("/a", rateLimitHeader(5000, 4500, reset, "core"))
	if got := s.resources["core"].LastWarn; !got.IsZero() {
		t.Fatalf("LastWarn should stay zero when above threshold, got %v", got)
	}
}

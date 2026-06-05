package github

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
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
	s.observeResponse("GET", "/some/path", http.Header{})
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

	s.observeResponse("GET", "/repos/x/y", rateLimitHeader(5000, 4500, reset, "core"))
	s.observeResponse("GET", "/search/code", rateLimitHeader(30, 25, reset, "search"))

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

	s.observeResponse("GET", "/a", rateLimitHeader(5000, 4500, reset, "core"))
	s.observeResponse("GET", "/a", rateLimitHeader(5000, 100, reset, "core"))
	s.observeResponse("GET", "/a", rateLimitHeader(5000, 200, reset, "core"))

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

	s.observeResponse("GET", "/a", rateLimitHeader(5000, 100, resetA, "core"))
	if got := s.resources["core"].LowestSeen; got != 100 {
		t.Fatalf("setup: lowestSeen=%d, want 100", got)
	}

	// New window — Remaining went back up and Reset advanced.
	s.observeResponse("GET", "/a", rateLimitHeader(5000, 4900, resetB, "core"))
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
	s.observeResponse("GET", "/a", rateLimitHeader(5000, 500, reset, "core"))
	firstWarn := s.resources["core"].LastWarn
	if firstWarn.IsZero() {
		t.Fatalf("expected LastWarn set after first sub-threshold response")
	}

	// Same time → throttle holds.
	s.observeResponse("GET", "/b", rateLimitHeader(5000, 400, reset, "core"))
	if got := s.resources["core"].LastWarn; !got.Equal(firstWarn) {
		t.Fatalf("LastWarn should not advance within throttle window: was %v, got %v", firstWarn, got)
	}

	// Advance just past the throttle interval.
	clock = clock.Add(rateLimitWarnInterval + time.Second)
	s.observeResponse("GET", "/c", rateLimitHeader(5000, 300, reset, "core"))
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

	s.observeResponse("GET", "/a", rateLimitHeader(5000, 4500, reset, "core"))
	if got := s.resources["core"].LastWarn; !got.IsZero() {
		t.Fatalf("LastWarn should stay zero when above threshold, got %v", got)
	}
}

func TestRateLimitState_CountsPerTemplatedPath(t *testing.T) {
	// The per-window heatmap is keyed by `METHOD pathTemplate(path)`.
	// Many distinct numeric ids must collapse into a single counter
	// row so a busy "GET /repos/.../pulls/{n}" loop shows up as one
	// hot entry, not thousands of unique ids.
	s := &rateLimitState{now: func() time.Time { return time.Unix(1_000_000, 0) }}
	reset := int(time.Unix(1_000_000, 0).Add(time.Hour).Unix())

	for _, n := range []int{1, 2, 3, 100, 12345} {
		s.observeResponse("GET", "/repos/o/r/pulls/"+strconv.Itoa(n), rateLimitHeader(5000, 4500, reset, "core"))
	}
	s.observeResponse("POST", "/repos/o/r/pulls/42/comments",
		rateLimitHeader(5000, 4499, reset, "core"))

	calls := s.resources["core"].Calls
	if got := calls["GET /repos/o/r/pulls/{n}"]; got != 5 {
		t.Fatalf("templated GET counter: got %d, want 5", got)
	}
	if got := calls["POST /repos/o/r/pulls/{n}/comments"]; got != 1 {
		t.Fatalf("templated POST counter: got %d, want 1", got)
	}
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 distinct templated keys, got %d (%v)", len(calls), calls)
	}
}

func TestRateLimitState_CountsResetOnWindowTransition(t *testing.T) {
	// Heatmap is per-window — when GitHub rolls the budget over, the
	// previous window's counts are logged in the summary and dropped.
	// Carrying them across would make the next warn line lie about
	// "what burned the budget in the CURRENT window".
	s := &rateLimitState{now: func() time.Time { return time.Unix(1_000_000, 0) }}
	resetA := int(time.Unix(1_000_000, 0).Add(time.Hour).Unix())
	resetB := int(time.Unix(1_000_000, 0).Add(2 * time.Hour).Unix())

	s.observeResponse("GET", "/repos/o/r/pulls/1", rateLimitHeader(5000, 4500, resetA, "core"))
	s.observeResponse("GET", "/repos/o/r/pulls/2", rateLimitHeader(5000, 4499, resetA, "core"))
	if got := s.resources["core"].Calls["GET /repos/o/r/pulls/{n}"]; got != 2 {
		t.Fatalf("setup: pre-transition counter=%d, want 2", got)
	}

	// Window flip. Single call in the new window — counters should
	// reflect only this call, not the carryover.
	s.observeResponse("GET", "/repos/o/r/issues/9", rateLimitHeader(5000, 4900, resetB, "core"))

	calls := s.resources["core"].Calls
	if got := calls["GET /repos/o/r/issues/{n}"]; got != 1 {
		t.Fatalf("new-window counter: got %d, want 1", got)
	}
	if got, ok := calls["GET /repos/o/r/pulls/{n}"]; ok {
		t.Fatalf("prior-window counter should have been dropped, still present with %d", got)
	}
}

func TestTopPaths_OrderingAndCap(t *testing.T) {
	// Top-K must be ordered by count desc, then key asc (stable
	// alphabetical tiebreak). Caller passes a k that may exceed
	// len(map); the helper must clamp without panicking and return
	// nil for an empty map so the slog field renders cleanly.
	m := map[string]int{
		"GET /repos/o/r/pulls/{n}":          50,
		"GET /repos/o/r/commits/{sha}":      50, // tie with pulls — alphabetical wins
		"POST /repos/o/r/issues":            10,
		"GET /repos/o/r/issues/{n}/labels": 5,
	}
	got := topPaths(m, 3)
	want := []string{
		"GET /repos/o/r/commits/{sha}:50",
		"GET /repos/o/r/pulls/{n}:50",
		"POST /repos/o/r/issues:10",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("top-K order: got %v, want %v", got, want)
	}

	// k > len(m) clamps; result is the entire sorted set.
	if got := topPaths(m, 99); len(got) != len(m) {
		t.Fatalf("oversized k should clamp to map size: got %d, want %d", len(got), len(m))
	}

	// Empty map → nil so the slog field renders as "[]".
	if got := topPaths(map[string]int{}, 5); got != nil {
		t.Fatalf("empty map should return nil, got %v", got)
	}
}

func TestPathTemplate_Normalizes(t *testing.T) {
	// Templating rules: keep owner/repo concrete; collapse numeric
	// segments to {n}; collapse 20+ hex segments to {sha}; strip
	// query strings.
	cases := []struct {
		in   string
		want string
	}{
		{"/repos/prellr/multica/pulls/182", "/repos/prellr/multica/pulls/{n}"},
		{"/repos/prellr/multica/commits/90d8d26130bb9953c69503c2c366ad9d8bc5b8c0/status",
			"/repos/prellr/multica/commits/{sha}/status"},
		{"/repos/prellr/multica/actions/runs/12345", "/repos/prellr/multica/actions/runs/{n}"},
		{"/app/installations/97531", "/app/installations/{n}"},
		{"/repos/prellr/multica/pulls?state=open&per_page=100", "/repos/prellr/multica/pulls"},
		{"/repos/prellr/multica", "/repos/prellr/multica"},
		// Short hex segment looks like a repo name — must stay literal.
		{"/repos/o/deadbeef", "/repos/o/deadbeef"},
		// Leading slash preserved; multiple slashes preserved.
		{"/", "/"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := pathTemplate(tc.in); got != tc.want {
			t.Errorf("pathTemplate(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSnapshot_RendersCurrentWindow(t *testing.T) {
	// snapshot() is the one-shot accessor a future caller (e.g. the
	// merge train on ErrRateLimited) can use to attach the heatmap
	// to its own pause log. Verifies the rendered string contains
	// the key fields and that an unknown resource returns "".
	s := &rateLimitState{now: func() time.Time { return time.Unix(1_000_000, 0) }}
	reset := int(time.Unix(1_000_000, 0).Add(30 * time.Minute).Unix())

	s.observeResponse("GET", "/repos/o/r/pulls/1", rateLimitHeader(5000, 100, reset, "core"))
	s.observeResponse("GET", "/repos/o/r/pulls/2", rateLimitHeader(5000, 99, reset, "core"))

	out := s.snapshot("core")
	for _, sub := range []string{
		"resource=core",
		"remaining=99",
		"limit=5000",
		"total_calls=2",
		"GET /repos/o/r/pulls/{n}:2",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("snapshot output missing %q\nfull: %s", sub, out)
		}
	}

	if got := s.snapshot("graphql"); got != "" {
		t.Errorf("snapshot for unobserved resource: got %q, want \"\"", got)
	}
	// Empty resource defaults to "core" so callers don't have to
	// hard-code the resource name.
	if got := s.snapshot(""); !strings.Contains(got, "resource=core") {
		t.Errorf("snapshot(\"\") should default to core; got %q", got)
	}
}

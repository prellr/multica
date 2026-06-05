package github

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate-limit observability for the GitHub REST client.
//
// Every GitHub response carries a window of X-RateLimit-* headers
// describing the remaining budget for the resource the request hit
// (core, search, graphql, ...). We snapshot those into a per-Client,
// per-resource state map so the operator gets three things for free:
//
//   1. Warn-level logs when remaining drops below a threshold (default
//      20 %). Throttled to one warn per resource per minute so a
//      sustained pressure window doesn't drown the rest of the logs.
//      The warn carries the top-K endpoints in the current window so
//      operators can pin "what burned the budget" without instrumenting
//      callers.
//
//   2. Info-level "window summary" logs when GitHub's reset boundary
//      crosses — we report the lowest remaining we saw during the
//      previous window plus the total call count and top-K endpoints.
//      Logged once per resource per window transition. Fires whether
//      we hit pressure or not so a quiet-baseline hour is also visible.
//
//   3. An on-demand snapshot() accessor that long-running flows can
//      call when they decide to pause due to ErrRateLimited — so the
//      "I paused because of rate limit" log line can be co-located
//      with the heatmap that explains why. Not yet wired from outside
//      this package; kept here so the integration is a one-liner the
//      day we need it.
//
// No behavior change: we only read headers, count, and emit log lines.
// The retry-with-backoff and PAT fallback paths in github.go's
// doWithBody are untouched. Cost per request is a handful of header
// lookups + a single map increment under a short-held mutex.

// rateLimitWarnThreshold is the fraction of the per-resource budget
// below which an observed Remaining triggers a warn. 0.20 = warn when
// budget drops below 20 % of the documented limit. Cheap to tune later
// if 20 % turns out to fire too eagerly (or too late) in practice.
const rateLimitWarnThreshold = 0.20

// rateLimitWarnInterval throttles repeated warns for the same resource.
// One minute is short enough to be useful during an incident (you'll
// see new warns as pressure persists) and long enough that a brief
// sustained-low window doesn't produce thousands of log lines.
const rateLimitWarnInterval = time.Minute

// rateLimitTopKWarn / rateLimitTopKSummary control how many endpoints
// are listed in the warn line vs the window-reset summary. Warn fires
// during an incident — we want the very hottest few inline. Summary
// fires once an hour — we can afford a longer list.
const (
	rateLimitTopKWarn    = 5
	rateLimitTopKSummary = 10
)

// rateLimitState is the per-resource snapshot the Client keeps for
// observability. Resources are GitHub's vocabulary: core (REST),
// search, graphql, etc. — see the X-RateLimit-Resource header.
//
// `lowestSeen` resets to `remaining` at each window boundary so the
// summary line reports the prior window's worst, not the all-time
// worst. `lastWarn` enforces the 1-min throttle. `calls` is the
// per-window heatmap, reset alongside lowestSeen.
type rateLimitState struct {
	mu        sync.Mutex
	resources map[string]*resourceWindow
	// now is injectable so tests can pin time without freezing the
	// production clock. Production callers leave it nil → time.Now.
	now func() time.Time
}

type resourceWindow struct {
	Limit      int
	Remaining  int
	Reset      time.Time
	LowestSeen int
	LastWarn   time.Time
	// Calls is the per-(method, normalized path-template) counter for
	// the current window. Reset on every window transition. Keys look
	// like "GET /repos/prellr/multica/pulls/{n}" — owner/repo stay
	// concrete (high operator value: "which repo is hot") while
	// numeric ids and long hex sha segments collapse to {n}/{sha}
	// via pathTemplate() so map cardinality stays bounded.
	Calls map[string]int
}

// nowFn returns the state's clock, falling back to time.Now when nil.
// Avoids a constructor + makes the zero value usable directly.
func (s *rateLimitState) nowFn() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// observeResponse parses the rate-limit headers from a GitHub response,
// records the call against the per-window heatmap, and emits log lines
// per the contract documented at the top of this file. Caller is
// doWithBody, which fires this once per HTTP response (every retry
// attempt included — the headers carry slightly different values when
// a retry catches a fresh reset, and a retry IS another call against
// the budget so it deserves its own counter increment).
//
// `method` and `path` are the request method/URL-path; they're combined
// via pathTemplate() into the heatmap key. `header` is the raw
// response Header (we read X-RateLimit-Limit/Remaining/Reset/Resource).
func (s *rateLimitState) observeResponse(method, path string, header http.Header) {
	limit, ok := parseIntHeader(header, "X-RateLimit-Limit")
	if !ok {
		return // GitHub didn't send the headers (unauthenticated / API
		// surfaces without rate limits — log nothing rather than
		// fabricate state from partial data)
	}
	remaining, ok := parseIntHeader(header, "X-RateLimit-Remaining")
	if !ok {
		return
	}
	resetUnix, ok := parseIntHeader(header, "X-RateLimit-Reset")
	if !ok {
		return
	}
	reset := time.Unix(int64(resetUnix), 0)
	resource := header.Get("X-RateLimit-Resource")
	if resource == "" {
		resource = "core" // REST default if the header is absent (older API surfaces)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.resources == nil {
		s.resources = map[string]*resourceWindow{}
	}
	prev, hadPrior := s.resources[resource]

	// Window transition: GitHub rolled the budget over. Log a summary
	// for the prior window before swapping state — this is the most
	// useful single line for "how tight did we get last hour" and
	// fires at most once per resource per window. Includes the
	// heatmap so a quiet-hour summary is still informative.
	if hadPrior && reset.After(prev.Reset) {
		slog.Info("github rate limit window reset",
			"resource", resource,
			"prev_lowest_remaining", prev.LowestSeen,
			"prev_limit", prev.Limit,
			"prev_total_calls", totalCalls(prev.Calls),
			"prev_top_paths", topPaths(prev.Calls, rateLimitTopKSummary),
			"new_reset_at", reset.Format(time.RFC3339),
		)
	}

	// Update state. LowestSeen is the running min within the current
	// window (or starts at the current Remaining when we're entering
	// a fresh window). Calls map is reset alongside; LastWarn is NOT
	// carried across windows so a new window with low budget gets an
	// immediate warn.
	cur := prev
	if !hadPrior || reset.After(prev.Reset) {
		cur = &resourceWindow{
			Limit:      limit,
			Remaining:  remaining,
			Reset:      reset,
			LowestSeen: remaining,
			Calls:      map[string]int{},
		}
	} else {
		cur.Limit = limit
		cur.Remaining = remaining
		if remaining < cur.LowestSeen {
			cur.LowestSeen = remaining
		}
	}
	if cur.Calls == nil {
		// Defensive: the zero-value rateLimitState path created a
		// window above with a non-nil Calls map, but a test or future
		// caller that constructs a resourceWindow directly might leave
		// it nil. Initializing here keeps the increment below safe.
		cur.Calls = map[string]int{}
	}
	cur.Calls[method+" "+pathTemplate(path)]++
	s.resources[resource] = cur

	// Warn when we're below threshold AND we haven't warned for this
	// resource within rateLimitWarnInterval. The first warn in a window
	// always fires; subsequent ones throttle. `top_paths` is the
	// "what burned the budget" answer — operators reading this log
	// line during an incident don't have to grep for separate counters.
	if limit > 0 && remaining < int(float64(limit)*rateLimitWarnThreshold) {
		now := s.nowFn()
		if cur.LastWarn.IsZero() || now.Sub(cur.LastWarn) >= rateLimitWarnInterval {
			slog.Warn("github rate limit low",
				"resource", resource,
				"remaining", remaining,
				"limit", limit,
				"total_calls", totalCalls(cur.Calls),
				"top_paths", topPaths(cur.Calls, rateLimitTopKWarn),
				"reset_at", reset.Format(time.RFC3339),
				"reset_in", time.Until(reset).Round(time.Second).String(),
				"path", path,
			)
			cur.LastWarn = now
		}
	}
}

// snapshot returns a single-line human-readable summary of the
// in-progress window for `resource` ("" → "core"). Suitable for
// one-shot diagnostic logging from callers that want to attach
// context next to a forced pause. Returns "" when no responses have
// been observed for the resource yet.
//
// Not yet called from outside this package — kept here so a future
// wire-up (e.g. the merge train logging the heatmap when it pauses
// on ErrRateLimited) is a one-liner.
func (s *rateLimitState) snapshot(resource string) string {
	if resource == "" {
		resource = "core"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.resources[resource]
	if !ok {
		return ""
	}
	return fmt.Sprintf("resource=%s remaining=%d limit=%d lowest_in_window=%d total_calls=%d reset_in=%s top=%v",
		resource,
		cur.Remaining,
		cur.Limit,
		cur.LowestSeen,
		totalCalls(cur.Calls),
		time.Until(cur.Reset).Round(time.Second).String(),
		topPaths(cur.Calls, rateLimitTopKWarn),
	)
}

// totalCalls sums the per-path counter map. Cheap (size is bounded by
// the number of distinct endpoint templates we hit in an hour — dozens
// at most thanks to pathTemplate normalization), so we don't keep a
// separate running counter.
func totalCalls(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// topPaths returns the top k entries by count, formatted as
// "METHOD path:count" strings. Sorted descending by count, then
// alphabetically on key to make the output stable when counts tie
// (important for test assertions and for human readability when
// scanning two adjacent log lines). Returns nil when the map is
// empty so the slog field renders as "[]" rather than "[<nil>]".
func topPaths(m map[string]int, k int) []string {
	if len(m) == 0 {
		return nil
	}
	type kv struct {
		key   string
		count int
	}
	xs := make([]kv, 0, len(m))
	for key, count := range m {
		xs = append(xs, kv{key, count})
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].count != xs[j].count {
			return xs[i].count > xs[j].count
		}
		return xs[i].key < xs[j].key
	})
	if k > len(xs) {
		k = len(xs)
	}
	out := make([]string, k)
	for i := 0; i < k; i++ {
		out[i] = fmt.Sprintf("%s:%d", xs[i].key, xs[i].count)
	}
	return out
}

// pathTemplate normalizes a GitHub REST path into a template so the
// per-window counter map stays bounded. Owner/repo segments stay
// concrete (cardinality is naturally bounded by your project count
// and the operator value of "which repo is hot" is high). Numeric
// segments collapse to "{n}"; long all-hex segments collapse to
// "{sha}". Query strings are stripped — they're noise for the
// heatmap and would blow up cardinality (cursor/per_page values).
//
// Examples:
//
//	/repos/prellr/multica/pulls/182                  → /repos/prellr/multica/pulls/{n}
//	/repos/prellr/multica/commits/90d8d26.../status  → /repos/prellr/multica/commits/{sha}/status
//	/repos/prellr/multica/actions/runs/12345         → /repos/prellr/multica/actions/runs/{n}
//	/app/installations/97531                         → /app/installations/{n}
func pathTemplate(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg == "" {
			continue
		}
		if isAllDigits(seg) {
			segs[i] = "{n}"
			continue
		}
		// Threshold of 20 protects against false positives — short
		// owner/repo names that happen to be all hex (e.g. a 7-char
		// repo name like "deadbee") stay literal. Real commit SHAs
		// are 40 hex chars; we accept ≥20 to also cover the short
		// refs GitHub sometimes echoes in /git/* responses.
		if len(seg) >= 20 && isAllHex(seg) {
			segs[i] = "{sha}"
		}
	}
	return strings.Join(segs, "/")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isAllHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// parseIntHeader pulls a single integer header value and reports
// whether parsing succeeded. Used for the X-RateLimit-* triple; a
// missing or unparseable value short-circuits observeResponse without
// touching the log so the caller never sees "fake zeros" downstream.
func parseIntHeader(h http.Header, name string) (int, bool) {
	raw := h.Get(name)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return v, true
}

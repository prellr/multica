package github

import (
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Rate-limit observability for the GitHub REST client.
//
// Every GitHub response carries a window of X-RateLimit-* headers
// describing the remaining budget for the resource the request hit
// (core, search, graphql, ...). We snapshot those into a per-Client,
// per-resource state map so the operator gets two things for free:
//
//   1. Warn-level logs when remaining drops below a threshold (default
//      20 %). Throttled to one warn per resource per minute so a
//      sustained pressure window doesn't drown the rest of the logs.
//
//   2. Info-level "window summary" logs when GitHub's reset boundary
//      crosses — we report the lowest remaining we saw during the
//      previous window, which is the actual "did we get close" signal.
//      Logged once per resource per window transition.
//
// No behavior change: we only read headers and emit log lines. The
// existing retry-with-backoff and PAT fallback paths in doWithBody are
// untouched. Cost per request is a handful of header lookups + map
// access under a short-held mutex.

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

// rateLimitState is the per-resource snapshot the Client keeps for
// observability. Resources are GitHub's vocabulary: core (REST),
// search, graphql, etc. — see the X-RateLimit-Resource header.
//
// `lowestSeen` resets to `remaining` at each window boundary so the
// summary line reports the prior window's worst, not the all-time
// worst. `lastWarn` enforces the 1-min throttle.
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
}

// nowFn returns the state's clock, falling back to time.Now when nil.
// Avoids a constructor + makes the zero value usable directly.
func (s *rateLimitState) nowFn() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// observeResponse parses the rate-limit headers from a GitHub response
// and emits log lines per the contract documented at the top of this
// file. Caller is doWithBody, which fires this once per HTTP response
// (every retry attempt included — the headers carry slightly different
// values when a retry catches a fresh reset).
//
// `path` is the request URL path (no host); used purely as a log field
// to correlate budget drops with the noisy code paths.
func (s *rateLimitState) observeResponse(path string, header http.Header) {
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
	// fires at most once per resource per window.
	if hadPrior && reset.After(prev.Reset) {
		slog.Info("github rate limit window reset",
			"resource", resource,
			"prev_lowest_remaining", prev.LowestSeen,
			"prev_limit", prev.Limit,
			"new_reset_at", reset.Format(time.RFC3339),
		)
	}

	// Update state. LowestSeen is the running min within the current
	// window (or starts at the current Remaining when we're entering
	// a fresh window). The bookkeeping is intentionally simple — we
	// don't track a per-second histogram or anything; the warn + reset
	// summary cover the practical observability needs.
	cur := prev
	if !hadPrior || reset.After(prev.Reset) {
		cur = &resourceWindow{
			Limit:      limit,
			Remaining:  remaining,
			Reset:      reset,
			LowestSeen: remaining,
			// LastWarn intentionally NOT carried across windows so a
			// new window with low budget gets an immediate warn.
		}
	} else {
		cur.Limit = limit
		cur.Remaining = remaining
		if remaining < cur.LowestSeen {
			cur.LowestSeen = remaining
		}
	}
	s.resources[resource] = cur

	// Warn when we're below threshold AND we haven't warned for this
	// resource within rateLimitWarnInterval. The first warn in a window
	// always fires; subsequent ones throttle.
	if limit > 0 && remaining < int(float64(limit)*rateLimitWarnThreshold) {
		now := s.nowFn()
		if cur.LastWarn.IsZero() || now.Sub(cur.LastWarn) >= rateLimitWarnInterval {
			slog.Warn("github rate limit low",
				"resource", resource,
				"remaining", remaining,
				"limit", limit,
				"reset_at", reset.Format(time.RFC3339),
				"reset_in", time.Until(reset).Round(time.Second).String(),
				"path", path,
			)
			cur.LastWarn = now
		}
	}
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

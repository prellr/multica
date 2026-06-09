package ship

import (
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// prRefreshCache short-circuits the per-PR `GET /repos/{owner}/{repo}/
// pulls/{N}` call when the same PR was successfully refreshed within
// the TTL window. Exists because the frontend useMergeablePoll hook
// fires `POST /api/pull_requests/{id}/refresh` for every Ship Hub card
// whose `mergeable === "UNKNOWN"`, every 10 seconds, up to 6 attempts
// per card. On a kanban with ~20 PRs that fan-out blows the per-token
// GitHub rate budget (verified via the PR #183 heatmap logs on
// 2026-06-09: a 23-PR kanban produced 23 simultaneous `pulls/{n}`
// detail fetches within a 49 ms span, repeated every 10 seconds).
//
// The TTL is intentionally just-under the frontend's 10 s poll
// interval so the worst case is "every other poll round trips to
// GitHub" — that is, 23 cards × 6 attempts collapses to ~3 GH calls
// per card instead of 6, roughly halving the burst without changing
// the user-visible mergeable-resolution latency. The frontend keeps
// receiving fresh responses on every poll; it just gets the cached
// row on the cycles in between.
//
// We deliberately do NOT cache by SHA or by mergeable value. A
// refresh that resolves a PR from UNKNOWN to MERGEABLE/CONFLICTING
// still has to be written to the row through UpdatePullRequestState,
// because downstream consumers (release stage gating, merge train
// eligibility) read the persisted row, not the in-memory cache. The
// cache is a request-deduplication mechanism, not a state store.
//
// Concurrency: a single sync.Mutex guards the whole map. The hot path
// is one map read and one timestamp compare; even hundreds of cards
// polling in lockstep on a single backend pod cannot generate enough
// contention here to matter. If it ever does, swap for sync.Map or a
// sharded mutex.
type prRefreshCache struct {
	mu      sync.Mutex
	entries map[pgtype.UUID]prRefreshEntry
	ttl     time.Duration
	// now is injectable so tests can pin time without freezing the
	// production clock. Production callers leave it nil → time.Now.
	now func() time.Time
}

type prRefreshEntry struct {
	pr       db.PullRequest
	cachedAt time.Time
}

// prRefreshTTL is the cache lifetime per PR. 30 s = the frontend poll
// fires three times within one cached entry's lifetime, so a kanban
// of 23 PRs producing 23 fan-out requests every 10 s collapses to
// ~8 actual GH calls per cycle (only the entries that expire each
// round trigger an upstream fetch). Tunable per environment via
// MULTICA_PR_REFRESH_CACHE_TTL but the default is the one that
// matches the frontend's poll cadence — making it shorter than 10 s
// defeats the cache entirely, longer than ~60 s makes the "mergeable
// settled" UX feel sticky.
const prRefreshTTL = 30 * time.Second

// prRefreshCacheInstance is the process-wide singleton. Service is
// constructed per-request via loadPullRequestForAction, so a per-Service
// field wouldn't survive the burst we're trying to flatten. A package
// var lives for the process lifetime, which is the correct lifetime
// for a request-dedup cache.
var prRefreshCacheInstance = &prRefreshCache{
	entries: map[pgtype.UUID]prRefreshEntry{},
	ttl:     prRefreshTTL,
}

// nowFn returns the cache's clock, falling back to time.Now when nil.
// Avoids a constructor + makes the zero value usable directly.
func (c *prRefreshCache) nowFn() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get returns the cached PR for id if the entry is still within TTL.
// An expired entry is opportunistically deleted on read so the map
// doesn't accumulate stale rows for PRs that stopped polling — there
// is no separate GC goroutine.
func (c *prRefreshCache) get(id pgtype.UUID) (db.PullRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return db.PullRequest{}, false
	}
	if c.nowFn().Sub(e.cachedAt) > c.ttl {
		delete(c.entries, id)
		return db.PullRequest{}, false
	}
	return e.pr, true
}

// put writes (or refreshes) the cached PR for id, stamping cachedAt
// with the cache's clock. Called after a successful GH fetch + DB
// write so the cached row matches what was persisted.
func (c *prRefreshCache) put(id pgtype.UUID, pr db.PullRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = prRefreshEntry{pr: pr, cachedAt: c.nowFn()}
}

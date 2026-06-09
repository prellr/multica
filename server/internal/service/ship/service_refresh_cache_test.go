package ship

import (
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// idFromHex builds a pgtype.UUID from a 32-hex-char string. The cache uses
// pgtype.UUID as a map key (its Bytes field is a [16]byte array, struct-
// comparable), so two scans of the same UUID string produce equal map keys
// without needing pointer identity.
func idFromHex(t *testing.T, hex string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	// pgtype.UUID.Scan accepts the dashed form too; rebuild that shape so
	// callers can pass a concise constant.
	dashed := hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
	if err := u.Scan(dashed); err != nil {
		t.Fatalf("scan uuid %q: %v", dashed, err)
	}
	return u
}

func newTestCache(now func() time.Time) *prRefreshCache {
	return &prRefreshCache{
		entries: map[pgtype.UUID]prRefreshEntry{},
		ttl:     30 * time.Second,
		now:     now,
	}
}

func TestPRRefreshCache_GetMissReturnsFalse(t *testing.T) {
	c := newTestCache(func() time.Time { return time.Unix(1_000_000, 0) })
	if _, ok := c.get(idFromHex(t, "11111111111111111111111111111111")); ok {
		t.Fatal("get on empty cache should report miss")
	}
}

func TestPRRefreshCache_PutThenGetReturnsRow(t *testing.T) {
	// The load-bearing happy path: a successful refresh puts the row,
	// the next poll within TTL gets it back without a GH round trip.
	c := newTestCache(func() time.Time { return time.Unix(1_000_000, 0) })
	id := idFromHex(t, "22222222222222222222222222222222")
	want := db.PullRequest{ID: id, Title: "test PR"}
	c.put(id, want)

	got, ok := c.get(id)
	if !ok {
		t.Fatal("get immediately after put should hit")
	}
	if got.Title != want.Title {
		t.Errorf("cached row title: got %q, want %q", got.Title, want.Title)
	}
}

func TestPRRefreshCache_ExpiredEntryReportsMiss(t *testing.T) {
	// After the TTL elapses, a get must report a miss so the caller
	// falls through to the GH refresh path. The expired entry is
	// opportunistically deleted on the same call — verified by the
	// follow-on get returning miss without us re-putting anything.
	t0 := time.Unix(1_000_000, 0)
	clock := t0
	c := newTestCache(func() time.Time { return clock })

	id := idFromHex(t, "33333333333333333333333333333333")
	c.put(id, db.PullRequest{ID: id})

	// Advance just past the 30 s TTL.
	clock = clock.Add(31 * time.Second)
	if _, ok := c.get(id); ok {
		t.Fatal("expired entry should report miss")
	}
	// Second get: the first miss should have evicted, so still miss.
	if _, ok := c.get(id); ok {
		t.Fatal("evicted entry should remain absent")
	}
}

func TestPRRefreshCache_WithinTTLStaysCached(t *testing.T) {
	// Boundary case: exactly at TTL the entry is still valid (the
	// expiry check uses > ttl, not >=). One nanosecond before
	// expiry must still hit.
	t0 := time.Unix(1_000_000, 0)
	clock := t0
	c := newTestCache(func() time.Time { return clock })

	id := idFromHex(t, "44444444444444444444444444444444")
	c.put(id, db.PullRequest{ID: id})

	clock = clock.Add(c.ttl)
	if _, ok := c.get(id); !ok {
		t.Errorf("entry at exactly TTL should still hit (boundary)")
	}
}

func TestPRRefreshCache_PutRefreshesTimestamp(t *testing.T) {
	// A successful refresh that happens just before TTL must reset the
	// entry's age — otherwise a steady stream of polls would all
	// expire together at the original write time, defeating the
	// "sliding window" cache behavior the frontend depends on.
	t0 := time.Unix(1_000_000, 0)
	clock := t0
	c := newTestCache(func() time.Time { return clock })

	id := idFromHex(t, "55555555555555555555555555555555")
	c.put(id, db.PullRequest{ID: id, Title: "v1"})

	clock = clock.Add(20 * time.Second)
	c.put(id, db.PullRequest{ID: id, Title: "v2"})

	// 20 s after v2 = 40 s after v1, still within TTL of v2.
	clock = clock.Add(20 * time.Second)
	got, ok := c.get(id)
	if !ok {
		t.Fatal("entry re-put within TTL should still hit after sliding window")
	}
	if got.Title != "v2" {
		t.Errorf("get returned %q, want %q (latest put wins)", got.Title, "v2")
	}
}

func TestPRRefreshCache_ConcurrentReadsAndWrites(t *testing.T) {
	// Stress the lock. Hundreds of goroutines hammer the same key —
	// the fast path the cache is designed for. No race, no panic.
	c := newTestCache(nil) // nil → time.Now() in production
	id := idFromHex(t, "66666666666666666666666666666666")
	pr := db.PullRequest{ID: id, Title: "concurrent"}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); c.put(id, pr) }()
		go func() { defer wg.Done(); c.get(id) }()
	}
	wg.Wait()

	// After the storm, the entry should still be present and intact.
	got, ok := c.get(id)
	if !ok {
		t.Fatal("entry should still be present after concurrent burst")
	}
	if got.Title != "concurrent" {
		t.Errorf("got %q, want %q", got.Title, "concurrent")
	}
}

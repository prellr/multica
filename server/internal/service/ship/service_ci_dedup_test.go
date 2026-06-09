package ship

import (
	"context"
	"errors"
	"testing"

	gh "github.com/multica-ai/multica/server/pkg/github"
)

// fetchOrCacheCIStatus is the small helper inside refreshPRCIStatus that
// owns the per-SHA dedup. These tests pin the behavior end-to-end without
// needing a real db.Queries — the dedup is the load-bearing piece for
// shrinking the per-SyncProject GitHub call count, and we want a
// regression net against accidental cache-miss changes.

func TestFetchOrCacheCIStatus_CacheHitSkipsGH(t *testing.T) {
	// The contract: a prior put for the same SHA returns the cached
	// status without a GH call. Pre-populating the cache is the
	// realistic shape — the previous iteration of the SyncProject
	// loop wrote it on a miss.
	calls := 0
	s := &Service{Github: &fakeGithub{
		getCIStatusFn: func(_ context.Context, _, _, _ string) (string, error) {
			calls++
			return "success", nil
		},
	}}
	cache := map[string]string{"abc123": "success"}

	got, err := s.fetchOrCacheCIStatus(context.Background(), "o", "r", "abc123", cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "success" {
		t.Errorf("status: got %q, want %q", got, "success")
	}
	if calls != 0 {
		t.Errorf("expected 0 GH calls on cache hit, got %d", calls)
	}
}

func TestFetchOrCacheCIStatus_CacheMissWritesAndCalls(t *testing.T) {
	// Miss path: the first call for a SHA must hit GH and write the
	// result back so the next call for the same SHA hits the cache.
	// This is the prerequisite for the dedup behavior in the
	// multi-PR test below.
	calls := 0
	s := &Service{Github: &fakeGithub{
		getCIStatusFn: func(_ context.Context, _, _, _ string) (string, error) {
			calls++
			return "failure", nil
		},
	}}
	cache := map[string]string{}

	got, err := s.fetchOrCacheCIStatus(context.Background(), "o", "r", "deadbeef", cache)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "failure" {
		t.Errorf("status: got %q, want %q", got, "failure")
	}
	if calls != 1 {
		t.Errorf("expected 1 GH call on cache miss, got %d", calls)
	}
	if cache["deadbeef"] != "failure" {
		t.Errorf("cache should be populated after miss, got %v", cache)
	}
}

func TestFetchOrCacheCIStatus_DedupsAcrossSharedSHA(t *testing.T) {
	// The real win this whole change exists for. 5 PRs with the same
	// head SHA — the steady-state pattern when a release branch is
	// retargeted or release-train automation opens many PRs from one
	// commit — must produce exactly ONE GetCIStatus call. The 2026-
	// 06-09 heatmap showed the un-deduped version of this path among
	// the hottest entries, contributing to the per-token rate budget
	// exhaustion.
	calls := 0
	s := &Service{Github: &fakeGithub{
		getCIStatusFn: func(_ context.Context, _, _, _ string) (string, error) {
			calls++
			return "success", nil
		},
	}}
	cache := map[string]string{}

	for i := 0; i < 5; i++ {
		if _, err := s.fetchOrCacheCIStatus(context.Background(), "o", "r", "sharedSHA", cache); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Errorf("expected 1 GH call across 5 PRs with shared SHA, got %d", calls)
	}
}

func TestFetchOrCacheCIStatus_NilCacheAlwaysFetches(t *testing.T) {
	// Backwards-compatibility guard: callers that don't pass a cache
	// (the contract says nil is acceptable) must always fetch — no
	// hidden global cache, no silent dedup across calls that didn't
	// opt in. Keeps the dedup behavior coupled to caller intent.
	calls := 0
	s := &Service{Github: &fakeGithub{
		getCIStatusFn: func(_ context.Context, _, _, _ string) (string, error) {
			calls++
			return "success", nil
		},
	}}

	for i := 0; i < 3; i++ {
		if _, err := s.fetchOrCacheCIStatus(context.Background(), "o", "r", "anySHA", nil); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if calls != 3 {
		t.Errorf("expected 3 GH calls with nil cache, got %d", calls)
	}
}

func TestFetchOrCacheCIStatus_FailedFetchIsNotCached(t *testing.T) {
	// A transient fetch failure must NOT pin an empty status for the
	// next PR that shares the SHA in the same sync. Otherwise a single
	// flaky GH response would silently downgrade the CI rollup on
	// every other PR sharing the SHA for the rest of the sync, which
	// is worse than just letting each retry independently.
	calls := 0
	s := &Service{Github: &fakeGithub{
		getCIStatusFn: func(_ context.Context, _, _, _ string) (string, error) {
			calls++
			return "", errors.New("transient")
		},
	}}
	cache := map[string]string{}

	if _, err := s.fetchOrCacheCIStatus(context.Background(), "o", "r", "flakySHA", cache); err == nil {
		t.Fatal("expected error to propagate")
	}
	if _, found := cache["flakySHA"]; found {
		t.Errorf("failed fetch should not be cached, but cache=%v", cache)
	}

	// Second call must retry — the cache miss path runs again.
	_, _ = s.fetchOrCacheCIStatus(context.Background(), "o", "r", "flakySHA", cache)
	if calls != 2 {
		t.Errorf("expected 2 GH calls (no caching of failure), got %d", calls)
	}
}

// _ = gh.PullRequest{} is a paranoia guard so a future import-reshuffle
// doesn't silently drop the alias.
var _ = gh.PullRequest{}

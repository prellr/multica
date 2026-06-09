import { useEffect, useRef } from "react";
import { useRefreshPullRequest } from "@multica/core/ship";

/** Poll interval while a PR's mergeable is unresolved. GitHub usually
 *  settles the merge computation within a couple of seconds; 10s gives it
 *  room without hammering the API. */
export const MERGEABLE_POLL_INTERVAL_MS = 10_000;

/** Width of the random initial-tick window. Each card delays its first
 *  poll by 0–5s ON TOP of the standard 10s grace period — so a kanban
 *  burst that mounts 20 cards in the same render spreads their first
 *  refresh across a 5s window instead of arriving in one ~50 ms thundering
 *  herd (the 2026-06-09 GitHub rate-limit incident). The grace period
 *  itself stays unchanged; this only desynchronizes simultaneous mounts.
 *  Subsequent ticks remain on the standard interval, but starting from
 *  the staggered first fire, so the desync persists for the full poll
 *  window. */
export const MERGEABLE_POLL_JITTER_MS = 5_000;

/** Hard cap on poll attempts. 6 × 10s ≈ 1 minute — if GitHub still hasn't
 *  computed mergeability by then, something is wrong upstream and a
 *  manual sync is the right escape hatch. Stopping also prevents a card
 *  that's permanently UNKNOWN (e.g. a deleted base branch) from polling
 *  forever. */
export const MERGEABLE_POLL_MAX_ATTEMPTS = 6;

/**
 * Drive a per-PR live refresh while a PR's `mergeable` is still
 * "computing".
 *
 * GitHub returns `mergeable: null` right after a PR opens — the merge is
 * computed asynchronously — and Ship maps that to `mergeable === "UNKNOWN"`.
 * Without re-polling, the PR card shows "computing" forever until the user
 * manually syncs the project. This hook closes that gap: while the passed
 * `mergeable` value is `"UNKNOWN"`, it calls
 * `POST /api/pull_requests/{id}/refresh` every 10s (with 0–5s random
 * initial jitter — see {@link MERGEABLE_POLL_JITTER_MS}), capped at 6
 * attempts, then stops.
 *
 * It is intentionally minimal — the hook only triggers refetches. The
 * refresh mutation patches the refreshed PR row into the TanStack Query
 * cache, so the card re-renders with the resolved `mergeable` and the
 * hook's own input changes away from `"UNKNOWN"`, which stops the loop.
 *
 * @param prId      The PR's UUID.
 * @param mergeable The PR's current mergeable value from the cached row.
 *                  Polling runs only while this is exactly `"UNKNOWN"`.
 */
export function useMergeablePoll(
  prId: string,
  mergeable: string | null | undefined,
): void {
  const refresh = useRefreshPullRequest(prId);
  // Keep the mutation in a ref so the interval effect doesn't re-subscribe
  // every render (useMutation returns a fresh object reference each time).
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  const shouldPoll = mergeable === "UNKNOWN" && !!prId;

  useEffect(() => {
    if (!shouldPoll) return;

    let attempts = 0;
    let interval: ReturnType<typeof setInterval> | undefined;
    const tick = () => {
      attempts += 1;
      if (attempts > MERGEABLE_POLL_MAX_ATTEMPTS) {
        if (interval !== undefined) clearInterval(interval);
        return;
      }
      // Skip overlapping triggers if a refresh is still in flight — the
      // next tick will pick it up. Cheap-cheap guard against a slow API.
      if (refreshRef.current.isPending) return;
      refreshRef.current.mutate();
    };

    // Stagger the FIRST tick by a per-card random offset in
    // [0, MERGEABLE_POLL_JITTER_MS). The 10s grace period stays intact
    // (we still wait INTERVAL_MS before the first fire); the jitter
    // only spreads the burst when a kanban mounts many UNKNOWN cards in
    // the same render. After the first fire we move to the standard
    // INTERVAL_MS cadence — the desync from the staggered start carries
    // through for the rest of the poll window.
    const jitter = Math.random() * MERGEABLE_POLL_JITTER_MS;
    const initialDelay = MERGEABLE_POLL_INTERVAL_MS + jitter;

    const startTimer = setTimeout(() => {
      tick();
      interval = setInterval(tick, MERGEABLE_POLL_INTERVAL_MS);
    }, initialDelay);

    return () => {
      clearTimeout(startTimer);
      if (interval !== undefined) clearInterval(interval);
    };
    // shouldPoll flips to false once mergeable resolves (the refresh
    // mutation patches the cache), which tears the interval down. prId is
    // included so switching PR identity restarts the attempt counter.
  }, [shouldPoll, prId]);
}

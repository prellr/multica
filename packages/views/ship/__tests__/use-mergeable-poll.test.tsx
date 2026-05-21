import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { renderHook, act } from "@testing-library/react";

// useMergeablePoll is a thin view-layer hook: it owns the 10s / 6-attempt
// interval and delegates the actual refetch to the core mutation
// `useRefreshPullRequest`. Per CLAUDE.md "Testing Rules" we mock
// `@multica/core` (never `next/*` — which doesn't exist in this package)
// and assert purely on the interval/cap behavior.

const mutate = vi.fn();
let isPending = false;

vi.mock("@multica/core/ship", () => ({
  useRefreshPullRequest: () => ({ mutate, isPending }),
}));

import {
  useMergeablePoll,
  MERGEABLE_POLL_INTERVAL_MS,
  MERGEABLE_POLL_MAX_ATTEMPTS,
} from "../hooks/use-mergeable-poll";

beforeEach(() => {
  vi.useFakeTimers();
  mutate.mockClear();
  isPending = false;
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useMergeablePoll", () => {
  it("does not poll when mergeable is already resolved", () => {
    renderHook(() => useMergeablePoll("pr-1", "MERGEABLE"));
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS * 3);
    });
    expect(mutate).not.toHaveBeenCalled();
  });

  it("does not poll when mergeable is CONFLICTING", () => {
    renderHook(() => useMergeablePoll("pr-1", "CONFLICTING"));
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS * 3);
    });
    expect(mutate).not.toHaveBeenCalled();
  });

  it("polls every interval while mergeable is UNKNOWN", () => {
    renderHook(() => useMergeablePoll("pr-1", "UNKNOWN"));
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS);
    });
    expect(mutate).toHaveBeenCalledTimes(1);
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS * 2);
    });
    expect(mutate).toHaveBeenCalledTimes(3);
  });

  it("stops polling after the attempt cap", () => {
    renderHook(() => useMergeablePoll("pr-1", "UNKNOWN"));
    // Run well past the cap — a couple of extra intervals must NOT
    // produce extra calls.
    act(() => {
      vi.advanceTimersByTime(
        MERGEABLE_POLL_INTERVAL_MS * (MERGEABLE_POLL_MAX_ATTEMPTS + 4),
      );
    });
    expect(mutate).toHaveBeenCalledTimes(MERGEABLE_POLL_MAX_ATTEMPTS);
  });

  it("skips a tick while a refresh is still in flight", () => {
    isPending = true;
    renderHook(() => useMergeablePoll("pr-1", "UNKNOWN"));
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS * 2);
    });
    // isPending stays true the whole time → every tick is skipped.
    expect(mutate).not.toHaveBeenCalled();
  });

  it("stops polling once mergeable resolves on a re-render", () => {
    const { rerender } = renderHook(
      ({ mergeable }: { mergeable: string }) =>
        useMergeablePoll("pr-1", mergeable),
      { initialProps: { mergeable: "UNKNOWN" } },
    );
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS);
    });
    expect(mutate).toHaveBeenCalledTimes(1);

    // The refresh resolved mergeable — the card re-renders with the new
    // value and the hook must tear the interval down.
    rerender({ mergeable: "MERGEABLE" });
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS * 3);
    });
    expect(mutate).toHaveBeenCalledTimes(1);
  });

  it("does not poll when prId is empty", () => {
    renderHook(() => useMergeablePoll("", "UNKNOWN"));
    act(() => {
      vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS * 3);
    });
    expect(mutate).not.toHaveBeenCalled();
  });
});

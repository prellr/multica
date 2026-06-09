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
  // Pin jitter to 0 in the timing-contract tests so the first fire lands
  // at exactly MERGEABLE_POLL_INTERVAL_MS. Jitter behavior itself is
  // verified separately below.
  vi.spyOn(Math, "random").mockReturnValue(0);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
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

  describe("jitter", () => {
    // The 2026-06-09 rate-limit incident: 23 simultaneous refresh POSTs
    // in a 49ms window because every UNKNOWN card on a freshly-mounted
    // kanban hit the 10s mark at the same instant. The jitter spreads
    // the FIRST tick across MERGEABLE_POLL_JITTER_MS so a burst of
    // mounts produces a burst of differently-timed first calls.

    it("delays the first fire by Math.random() * JITTER on top of INTERVAL", () => {
      // Math.random() = 0.5 → jitter = 2500ms.
      // Override the beforeEach spy for this test.
      vi.spyOn(Math, "random").mockReturnValue(0.5);

      renderHook(() => useMergeablePoll("pr-jitter-mid", "UNKNOWN"));

      // At INTERVAL_MS exactly, no fire yet (jitter pushed it later).
      act(() => {
        vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS);
      });
      expect(mutate).not.toHaveBeenCalled();

      // Just before INTERVAL_MS + 2500ms: still nothing.
      act(() => {
        vi.advanceTimersByTime(2499);
      });
      expect(mutate).not.toHaveBeenCalled();

      // Cross the 2500ms jitter mark: first fire.
      act(() => {
        vi.advanceTimersByTime(1);
      });
      expect(mutate).toHaveBeenCalledTimes(1);
    });

    it("uses near-max jitter when Math.random() approaches 1", () => {
      // 0.998 → jitter 4990ms — close to the upper edge of the window.
      // Pick a draw with integer-clean math so fake-timer rounding
      // doesn't make the test brittle.
      vi.spyOn(Math, "random").mockReturnValue(0.998);
      const expectedDelay = MERGEABLE_POLL_INTERVAL_MS + 4990;

      renderHook(() => useMergeablePoll("pr-jitter-max", "UNKNOWN"));

      // One ms before the scheduled fire: nothing yet.
      act(() => {
        vi.advanceTimersByTime(expectedDelay - 1);
      });
      expect(mutate).not.toHaveBeenCalled();

      // Cross the threshold: first fire.
      act(() => {
        vi.advanceTimersByTime(1);
      });
      expect(mutate).toHaveBeenCalledTimes(1);
    });

    it("after the staggered first fire, ticks continue at the standard INTERVAL", () => {
      // 0.4 → jitter 2000ms. First fire at 12s.
      vi.spyOn(Math, "random").mockReturnValue(0.4);

      renderHook(() => useMergeablePoll("pr-jitter-cadence", "UNKNOWN"));

      act(() => {
        vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS + 2000);
      });
      expect(mutate).toHaveBeenCalledTimes(1);

      // Next tick is INTERVAL_MS later, NOT JITTER_MS later — the
      // jitter is one-shot on mount, not per-tick.
      act(() => {
        vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS);
      });
      expect(mutate).toHaveBeenCalledTimes(2);

      act(() => {
        vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS);
      });
      expect(mutate).toHaveBeenCalledTimes(3);
    });

    it("staggers a burst of card mounts across the jitter window", () => {
      // The actual incident scenario: many cards mount at once. With a
      // varying Math.random() the first POSTs should land at distinct
      // times in [INTERVAL, INTERVAL + JITTER). We can't easily prove
      // 20 distinct times in a single test, but we can prove two cards
      // with different random draws fire at different times — which is
      // the property that fixes the burst.
      const randomSpy = vi.spyOn(Math, "random");
      randomSpy.mockReturnValueOnce(0.0); // early card → fires at INTERVAL_MS
      randomSpy.mockReturnValueOnce(0.8); // late card → fires at INTERVAL_MS + 4000ms

      renderHook(() => useMergeablePoll("pr-early", "UNKNOWN"));
      renderHook(() => useMergeablePoll("pr-late", "UNKNOWN"));

      // At the standard interval the early card fires; the late card
      // has not yet.
      act(() => {
        vi.advanceTimersByTime(MERGEABLE_POLL_INTERVAL_MS);
      });
      expect(mutate).toHaveBeenCalledTimes(1);

      // Advance through the late card's jitter window.
      act(() => {
        vi.advanceTimersByTime(4000);
      });
      expect(mutate).toHaveBeenCalledTimes(2);
    });
  });
});

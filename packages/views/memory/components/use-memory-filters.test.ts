// Coverage for the memory-page filter hook. The triage flow is the
// product's most-trodden path right now — getting the persistence and
// preset semantics right is what makes "open an artifact, come back,
// pick up where you left off" feel smooth instead of frustrating.

import { describe, it, expect, beforeEach } from "vitest";
import { act, renderHook } from "@testing-library/react";

import { useMemoryFilters } from "./use-memory-filters";

const WS_A = "ws-a";
const WS_B = "ws-b";

beforeEach(() => {
  window.localStorage.clear();
});

describe("useMemoryFilters", () => {
  it("starts at defaults when nothing is persisted", () => {
    const { result } = renderHook(() => useMemoryFilters(WS_A));
    expect(result.current.state).toEqual({
      kindFilter: "all",
      selectedTags: [],
      showArchived: false,
      showSystem: false,
      unverifiedOnly: false,
    });
    expect(result.current.activeFilterCount).toBe(0);
  });

  it("activeFilterCount tracks every dimension", () => {
    const { result } = renderHook(() => useMemoryFilters(WS_A));
    act(() => result.current.setKindFilter("decision"));
    expect(result.current.activeFilterCount).toBe(1);
    act(() => result.current.toggleTag("mined"));
    expect(result.current.activeFilterCount).toBe(2);
    act(() => result.current.setShowArchived(true));
    act(() => result.current.setShowSystem(true));
    act(() => result.current.setUnverifiedOnly(true));
    expect(result.current.activeFilterCount).toBe(5);
  });

  it("toggleTag adds and removes idempotently", () => {
    const { result } = renderHook(() => useMemoryFilters(WS_A));
    act(() => result.current.toggleTag("infra"));
    expect(result.current.state.selectedTags).toEqual(["infra"]);
    act(() => result.current.toggleTag("auth"));
    expect(result.current.state.selectedTags).toEqual(["infra", "auth"]);
    act(() => result.current.toggleTag("infra"));
    expect(result.current.state.selectedTags).toEqual(["auth"]);
  });

  it("clearAll resets every dimension", () => {
    const { result } = renderHook(() => useMemoryFilters(WS_A));
    act(() => {
      result.current.setKindFilter("decision");
      result.current.toggleTag("mined");
      result.current.setShowArchived(true);
      result.current.setUnverifiedOnly(true);
    });
    expect(result.current.activeFilterCount).toBe(4);
    act(() => result.current.clearAll());
    expect(result.current.activeFilterCount).toBe(0);
  });

  it("applyTriagePreset sets mined+unverified and clears the rest", () => {
    const { result } = renderHook(() => useMemoryFilters(WS_A));
    // Pre-seed unrelated filters that the preset should replace.
    act(() => {
      result.current.setKindFilter("wiki_page");
      result.current.setShowArchived(true);
    });
    act(() => result.current.applyTriagePreset());
    expect(result.current.state).toEqual({
      kindFilter: "all",
      selectedTags: ["mined"],
      showArchived: false,
      showSystem: false,
      unverifiedOnly: true,
    });
  });

  it("persists to localStorage and rehydrates across mounts", () => {
    const { result, unmount } = renderHook(() => useMemoryFilters(WS_A));
    act(() => {
      result.current.setKindFilter("runbook");
      result.current.toggleTag("deploy");
      result.current.setUnverifiedOnly(true);
    });
    unmount();
    // Fresh mount — values should restore from localStorage.
    const { result: rehydrated } = renderHook(() => useMemoryFilters(WS_A));
    expect(rehydrated.current.state.kindFilter).toBe("runbook");
    expect(rehydrated.current.state.selectedTags).toEqual(["deploy"]);
    expect(rehydrated.current.state.unverifiedOnly).toBe(true);
  });

  it("scopes per-workspace — switching wsId loads a different bucket", () => {
    const { result, rerender } = renderHook(
      ({ wsId }: { wsId: string }) => useMemoryFilters(wsId),
      { initialProps: { wsId: WS_A } },
    );
    act(() => {
      result.current.setKindFilter("decision");
      result.current.toggleTag("ws-a-tag");
    });
    rerender({ wsId: WS_B });
    // Workspace B has no saved state → defaults.
    expect(result.current.state).toEqual({
      kindFilter: "all",
      selectedTags: [],
      showArchived: false,
      showSystem: false,
      unverifiedOnly: false,
    });
    // Set workspace B's state, then switch back — workspace A still
    // has its own (separate localStorage key).
    act(() => result.current.setKindFilter("wiki_page"));
    rerender({ wsId: WS_A });
    expect(result.current.state.kindFilter).toBe("decision");
    expect(result.current.state.selectedTags).toEqual(["ws-a-tag"]);
  });

  it("survives malformed persisted blob without throwing", () => {
    window.localStorage.setItem(
      "multica:memory:filters:" + WS_A,
      "{ this is not: valid json",
    );
    const { result } = renderHook(() => useMemoryFilters(WS_A));
    expect(result.current.state.kindFilter).toBe("all");
    expect(result.current.activeFilterCount).toBe(0);
  });

  it("loads partial old blobs by defaulting missing fields", () => {
    // A blob from an older version that didn't yet have
    // `unverifiedOnly` — should load cleanly with that field false.
    window.localStorage.setItem(
      "multica:memory:filters:" + WS_A,
      JSON.stringify({ kindFilter: "decision", selectedTags: ["mined"] }),
    );
    const { result } = renderHook(() => useMemoryFilters(WS_A));
    expect(result.current.state.kindFilter).toBe("decision");
    expect(result.current.state.selectedTags).toEqual(["mined"]);
    expect(result.current.state.unverifiedOnly).toBe(false);
    expect(result.current.state.showArchived).toBe(false);
  });
});

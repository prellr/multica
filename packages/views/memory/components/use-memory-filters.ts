import { useCallback, useEffect, useState } from "react";
import type { MemoryArtifactKind } from "@multica/core/types";

// Shape of every persisted filter on the memory page. Keep narrow and
// flat — a JSON.stringify of this object is what lands in localStorage
// per-workspace, so adding a field is a forwards-compatible change as
// long as `load()` defaults it correctly for older saved blobs.
export interface MemoryFiltersState {
  kindFilter: MemoryArtifactKind | "all";
  selectedTags: string[];
  showArchived: boolean;
  showSystem: boolean;
  unverifiedOnly: boolean;
}

const DEFAULT_STATE: MemoryFiltersState = {
  kindFilter: "all",
  selectedTags: [],
  showArchived: false,
  showSystem: false,
  unverifiedOnly: false,
};

// Per-workspace key — switching workspaces should NOT carry the prior
// workspace's triage state into the new context. Mirrors how task
// filters and view modes scope themselves elsewhere in the app.
function storageKey(wsId: string): string {
  return `multica:memory:filters:${wsId}`;
}

function load(wsId: string): MemoryFiltersState {
  if (typeof window === "undefined") return DEFAULT_STATE;
  try {
    const raw = window.localStorage.getItem(storageKey(wsId));
    if (!raw) return DEFAULT_STATE;
    const parsed = JSON.parse(raw) as Partial<MemoryFiltersState>;
    // Default each field individually so a partial old blob (one that
    // predates a new filter being added) still loads cleanly.
    return {
      kindFilter: parsed.kindFilter ?? "all",
      selectedTags: Array.isArray(parsed.selectedTags)
        ? parsed.selectedTags
        : [],
      showArchived: !!parsed.showArchived,
      showSystem: !!parsed.showSystem,
      unverifiedOnly: !!parsed.unverifiedOnly,
    };
  } catch {
    return DEFAULT_STATE;
  }
}

function save(wsId: string, state: MemoryFiltersState): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(storageKey(wsId), JSON.stringify(state));
  } catch {
    // localStorage can throw in private-browsing / quota-exceeded
    // contexts. Filter persistence isn't critical enough to surface
    // an error toast — fail silently and the next session starts at
    // defaults.
  }
}

// Counts how many filters are currently "doing something." Used for
// the active-filter summary chip ("3 filters active · Clear"). All
// dimensions that change what the user sees count — toggling Archived
// or Logs on is just as worth signposting as picking a kind, because
// the user may have forgotten and wonder why an archived row showed up.
function countActive(s: MemoryFiltersState): number {
  let n = 0;
  if (s.kindFilter !== "all") n++;
  if (s.selectedTags.length > 0) n++;
  if (s.showArchived) n++;
  if (s.showSystem) n++;
  if (s.unverifiedOnly) n++;
  return n;
}

export interface UseMemoryFiltersReturn {
  state: MemoryFiltersState;
  setKindFilter: (k: MemoryArtifactKind | "all") => void;
  setSelectedTags: (tags: string[]) => void;
  toggleTag: (tag: string) => void;
  setShowArchived: (v: boolean) => void;
  setShowSystem: (v: boolean) => void;
  setUnverifiedOnly: (v: boolean) => void;
  clearAll: () => void;
  applyTriagePreset: () => void;
  activeFilterCount: number;
}

// Source of truth for the memory page's filter UI. Lives in views (not
// core) because (a) packages/core's "zero localStorage" rule, and (b)
// this is purely page-local UI state that doesn't need to be shared
// with other apps or hooks.
export function useMemoryFilters(wsId: string): UseMemoryFiltersReturn {
  // Initialize from localStorage on first render — useState's lazy
  // initializer only runs once per mount, so this is the right place.
  const [state, setState] = useState<MemoryFiltersState>(() => load(wsId));

  // Re-load when the user switches workspaces (we don't want triage
  // filters from workspace A bleeding into workspace B). The mount
  // initializer above handles first-load; this handles transitions.
  useEffect(() => {
    setState(load(wsId));
  }, [wsId]);

  // Persist every change. Cheap; localStorage write is synchronous and
  // fast under 1KB. The dep array intentionally includes wsId so a
  // workspace switch persists into the right bucket.
  useEffect(() => {
    save(wsId, state);
  }, [wsId, state]);

  const setKindFilter = useCallback(
    (kindFilter: MemoryArtifactKind | "all") =>
      setState((s) => ({ ...s, kindFilter })),
    [],
  );
  const setSelectedTags = useCallback(
    (selectedTags: string[]) => setState((s) => ({ ...s, selectedTags })),
    [],
  );
  const toggleTag = useCallback(
    (tag: string) =>
      setState((s) => ({
        ...s,
        selectedTags: s.selectedTags.includes(tag)
          ? s.selectedTags.filter((t) => t !== tag)
          : [...s.selectedTags, tag],
      })),
    [],
  );
  const setShowArchived = useCallback(
    (showArchived: boolean) => setState((s) => ({ ...s, showArchived })),
    [],
  );
  const setShowSystem = useCallback(
    (showSystem: boolean) => setState((s) => ({ ...s, showSystem })),
    [],
  );
  const setUnverifiedOnly = useCallback(
    (unverifiedOnly: boolean) => setState((s) => ({ ...s, unverifiedOnly })),
    [],
  );
  const clearAll = useCallback(() => setState(DEFAULT_STATE), []);
  // Triage queue preset — the single most common workflow for the
  // miner+polisher loop: "show me the unverified mined candidates I
  // haven't triaged yet." Two clicks → one.
  const applyTriagePreset = useCallback(
    () =>
      setState({
        ...DEFAULT_STATE,
        selectedTags: ["mined"],
        unverifiedOnly: true,
      }),
    [],
  );

  return {
    state,
    setKindFilter,
    setSelectedTags,
    toggleTag,
    setShowArchived,
    setShowSystem,
    setUnverifiedOnly,
    clearAll,
    applyTriagePreset,
    activeFilterCount: countActive(state),
  };
}

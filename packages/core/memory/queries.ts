import { queryOptions, infiniteQueryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type {
  ListMemoryArtifactsParams,
  MemoryArtifactAnchorType,
  MemoryArtifactKind,
  MemoryArtifactLinkRelationType,
  MemoryArtifactLinkTargetType,
  SearchMemoryArtifactsParams,
} from "../types";

// How many artifacts each infinite-scroll page fetches. 50 keeps the first
// paint fast while the IntersectionObserver sentinel pulls more as the user
// scrolls. Matches the server's default/cap semantics (default 50, cap 200).
export const MEMORY_PAGE_SIZE = 50;

// Cache key factory. The wsId prefix makes workspace switching automatic
// — the cache key changes when wsId changes, so the right page renders
// without manual invalidation. Filter-bearing keys nest under `list`
// so any list invalidation hits all variants.
export const memoryKeys = {
  all: (wsId: string) => ["memory", wsId] as const,
  list: (wsId: string, params?: ListMemoryArtifactsParams) =>
    [...memoryKeys.all(wsId), "list", params ?? {}] as const,
  listInfinite: (wsId: string, params?: ListMemoryArtifactsParams) =>
    [...memoryKeys.all(wsId), "list-infinite", params ?? {}] as const,
  detail: (wsId: string, id: string) =>
    [...memoryKeys.all(wsId), "detail", id] as const,
  byAnchor: (wsId: string, anchorType: MemoryArtifactAnchorType, anchorId: string) =>
    [...memoryKeys.all(wsId), "by-anchor", anchorType, anchorId] as const,
  search: (wsId: string, params: SearchMemoryArtifactsParams) =>
    [...memoryKeys.all(wsId), "search", params] as const,
  searchInfinite: (wsId: string, params: SearchMemoryArtifactsParams) =>
    [...memoryKeys.all(wsId), "search-infinite", params] as const,
  tags: (wsId: string) => [...memoryKeys.all(wsId), "tags"] as const,
  // Relation graph — outgoing links from a specific artifact, and
  // incoming backlinks to an arbitrary entity.
  links: (wsId: string, artifactId: string) =>
    [...memoryKeys.all(wsId), "links", artifactId] as const,
  backlinks: (wsId: string, targetType: string, targetId: string) =>
    [...memoryKeys.all(wsId), "backlinks", targetType, targetId] as const,
};

export function memoryListOptions(
  wsId: string,
  params?: ListMemoryArtifactsParams,
) {
  return queryOptions({
    queryKey: memoryKeys.list(wsId, params),
    queryFn: () => api.listMemoryArtifacts(params),
    select: (data) => data.memory_artifacts,
  });
}

export function memoryDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: memoryKeys.detail(wsId, id),
    queryFn: () => api.getMemoryArtifact(id),
  });
}

export function memoryByAnchorOptions(
  wsId: string,
  anchorType: MemoryArtifactAnchorType,
  anchorId: string,
  params?: { limit?: number },
) {
  return queryOptions({
    queryKey: memoryKeys.byAnchor(wsId, anchorType, anchorId),
    queryFn: () => api.listMemoryArtifactsByAnchor(anchorType, anchorId, params),
    select: (data) => data.memory_artifacts,
  });
}

// Search is a separate cache space so toggling between list and search
// views doesn't trample either's results. Empty `q` is rejected at the
// server, so don't enable this query without a non-empty term — callers
// gate enablement via `enabled: q.trim().length > 0`.
export function memorySearchOptions(
  wsId: string,
  params: SearchMemoryArtifactsParams,
) {
  return queryOptions({
    queryKey: memoryKeys.search(wsId, params),
    queryFn: () => api.searchMemoryArtifacts(params),
    select: (data) => data.memory_artifacts,
  });
}

// Infinite-scroll variant of the list. Each page fetches MEMORY_PAGE_SIZE
// rows at an increasing offset; getNextPageParam stops once the rows loaded
// so far reach the server's reported total. The page's `limit`/`offset` are
// managed here, so callers pass only the filter params (kind/tags/anchor/...).
export function memoryListInfiniteOptions(
  wsId: string,
  params?: ListMemoryArtifactsParams,
) {
  return infiniteQueryOptions({
    queryKey: memoryKeys.listInfinite(wsId, params),
    queryFn: ({ pageParam }) =>
      api.listMemoryArtifacts({
        ...params,
        limit: MEMORY_PAGE_SIZE,
        offset: pageParam,
      }),
    initialPageParam: 0,
    getNextPageParam: (lastPage, allPages) => {
      const loaded = allPages.reduce(
        (n, p) => n + p.memory_artifacts.length,
        0,
      );
      return loaded < lastPage.total ? loaded : undefined;
    },
  });
}

// Infinite-scroll variant of search. Same pagination contract as the list;
// gate enablement on a non-empty `q` (the server rejects empty queries).
export function memorySearchInfiniteOptions(
  wsId: string,
  params: SearchMemoryArtifactsParams,
) {
  return infiniteQueryOptions({
    queryKey: memoryKeys.searchInfinite(wsId, params),
    queryFn: ({ pageParam }) =>
      api.searchMemoryArtifacts({
        ...params,
        limit: MEMORY_PAGE_SIZE,
        offset: pageParam,
      }),
    initialPageParam: 0,
    getNextPageParam: (lastPage, allPages) => {
      const loaded = allPages.reduce(
        (n, p) => n + p.memory_artifacts.length,
        0,
      );
      return loaded < lastPage.total ? loaded : undefined;
    },
  });
}

// Top workspace tags for the filter bar's autocomplete. Cheap aggregate;
// refetched lazily — a stale-by-a-few-minutes tag list is harmless.
export function memoryTagsOptions(wsId: string, limit = 50) {
  return queryOptions({
    queryKey: memoryKeys.tags(wsId),
    queryFn: () => api.listMemoryTags(limit),
    select: (data) => data.tags,
  });
}

// Canonical ordering of the human-creatable / human-filterable kinds. Used
// by the create modal's kind picker and the list filter pills. System kinds
// (session, dispatch_event) are intentionally NOT here — they're written by
// the squad runtime, not hand-created, and surface only behind the memory
// page's "show orchestrator logs" toggle. Keep stable; new kinds appended.
export const MEMORY_KINDS: readonly MemoryArtifactKind[] = [
  "wiki_page",
  "agent_note",
  "runbook",
  "decision",
] as const;

// System / orchestrator-log kinds — surfaced separately from MEMORY_KINDS so
// the create picker never offers them and the default list hides them.
export const MEMORY_SYSTEM_KINDS: readonly MemoryArtifactKind[] = [
  "session",
  "dispatch_event",
] as const;

// Personal kinds — anchored to a specific member, not the workspace as a
// whole. Hidden from the default memory list (same policy as system kinds),
// surfaced through the per-user Agent preferences settings panel instead.
export const MEMORY_PERSONAL_KINDS: readonly MemoryArtifactKind[] = [
  "preference",
] as const;

// Display labels — short, human-friendly. Used by list filters and
// detail-page headers. Must cover every MemoryArtifactKind (including system
// kinds, which still render a label wherever they appear).
export const MEMORY_KIND_LABELS: Record<MemoryArtifactKind, string> = {
  wiki_page: "Wiki",
  agent_note: "Agent note",
  runbook: "Runbook",
  decision: "Decision",
  session: "Session",
  dispatch_event: "Dispatch event",
  preference: "Preference",
};

// ---------------------------------------------------------------------------
// Relation graph — memory_artifact_link
// ---------------------------------------------------------------------------

// Outgoing links from a specific artifact. Powers the detail page's
// "Links" section.
export function memoryLinksOptions(wsId: string, artifactId: string) {
  return queryOptions({
    queryKey: memoryKeys.links(wsId, artifactId),
    queryFn: () => api.listMemoryArtifactLinks(artifactId),
    select: (data) => data.links,
  });
}

// Display labels for the relation type dropdown + the link badge.
// Keep stable; new relation types append. Must cover every
// MemoryArtifactLinkRelationType.
export const MEMORY_LINK_RELATION_LABELS: Record<
  MemoryArtifactLinkRelationType,
  string
> = {
  cites: "Cites",
  supersedes: "Supersedes",
  contradicts: "Contradicts",
  implements: "Implements",
  scope: "Scope",
  "discussed-in": "Discussed in",
  informs: "Informs",
};

// Canonical ordering for the create-link dropdown — most-useful first
// (cites is the broadest; supersedes/contradicts/implements are the
// architectural-decision relations).
export const MEMORY_LINK_RELATIONS: readonly MemoryArtifactLinkRelationType[] =
  [
    "cites",
    "supersedes",
    "contradicts",
    "implements",
    "scope",
    "discussed-in",
    "informs",
  ] as const;

// Target types the create-link dialog offers. memory_artifact comes
// first because the artifact-to-artifact case is the killer new use
// case (supersedes / contradicts).
export const MEMORY_LINK_TARGET_TYPES: readonly MemoryArtifactLinkTargetType[] =
  ["memory_artifact", "issue", "project", "agent", "channel"] as const;

export const MEMORY_LINK_TARGET_LABELS: Record<
  MemoryArtifactLinkTargetType,
  string
> = {
  memory_artifact: "Memory artifact",
  issue: "Issue",
  project: "Project",
  agent: "Agent",
  channel: "Channel",
};

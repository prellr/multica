import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * Draft query keys. Workspace scoping is part of the key so switching
 * workspaces automatically swaps the visible draft list with no manual
 * invalidation. Drafts are owner-scoped server-side (the API only returns the
 * requesting user's drafts), so there's no per-filter list dimension in slice 0
 * — `list` takes only `wsId`.
 */
export const draftKeys = {
  all: (wsId: string) => ["drafts", wsId] as const,
  /**
   * Prefix that matches the list cache for this workspace. Used by the cache
   * helpers / WS updaters for list-shaped patching.
   *
   * Do NOT use {@link all} as the prefix for setQueriesData — it would also
   * match {@link detail} caches (a single Draft), and the list-shape helpers
   * would read `old.drafts` as undefined and throw. Always go through `lists`
   * for list-shaped patching and `detail` for per-draft patching.
   */
  lists: (wsId: string) => [...draftKeys.all(wsId), "list"] as const,
  list: (wsId: string) => [...draftKeys.lists(wsId)] as const,
  detail: (wsId: string, id: string) =>
    [...draftKeys.all(wsId), "detail", id] as const,
};

/**
 * Workspace-scoped draft list (newest-edited first, server-ordered). A single
 * flat query — drafts have no status buckets or filters in slice 0. The
 * `select` layer extracts the array so consumers don't have to remember the
 * response wrapper shape.
 */
export function draftListOptions(wsId: string) {
  return queryOptions({
    queryKey: draftKeys.list(wsId),
    queryFn: () => api.listDrafts({ workspace_id: wsId }),
    select: (data) => data.drafts,
  });
}

export function draftDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: draftKeys.detail(wsId, id),
    queryFn: () => api.getDraft(id),
  });
}

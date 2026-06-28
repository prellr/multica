import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * Draft-annotation query keys. Keyed on wsId (so workspace switching swaps the
 * cache automatically — the hard rule) AND draftId (annotations are per-draft).
 * The list query is the only annotation read; create/update/delete/add-message
 * patch this one cache optimistically.
 */
export const draftAnnotationKeys = {
  all: (wsId: string) => ["draft-annotations", wsId] as const,
  /** All annotation caches for a draft (just the one list, but a prefix keeps
   *  it consistent with the other domains' key factories). */
  lists: (wsId: string, draftId: string) =>
    [...draftAnnotationKeys.all(wsId), draftId, "list"] as const,
  list: (wsId: string, draftId: string) =>
    [...draftAnnotationKeys.lists(wsId, draftId)] as const,
};

/**
 * A draft's annotations (oldest-first, server-ordered), each with its full
 * thread. The `select` layer extracts the array so consumers don't carry the
 * response wrapper.
 */
export function draftAnnotationListOptions(wsId: string, draftId: string) {
  return queryOptions({
    queryKey: draftAnnotationKeys.list(wsId, draftId),
    queryFn: () => api.listDraftAnnotations(draftId),
    select: (data) => data.annotations,
    // Only fetch once a real draft is selected (not a temp/optimistic id or an
    // empty selection).
    enabled: Boolean(draftId),
  });
}

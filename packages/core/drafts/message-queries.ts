import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/**
 * Draft conversation-rail query keys. Keyed on wsId (so workspace switching
 * swaps the cache automatically — the hard rule) AND draftId (the rail is
 * per-draft). The list query is the only rail read; the add-message mutation
 * patches this one cache optimistically.
 */
export const draftMessageKeys = {
  all: (wsId: string) => ["draft-messages", wsId] as const,
  /** All conversation caches for a draft (just the one list, but a prefix keeps
   *  it consistent with the other domains' key factories). */
  lists: (wsId: string, draftId: string) =>
    [...draftMessageKeys.all(wsId), draftId, "list"] as const,
  list: (wsId: string, draftId: string) =>
    [...draftMessageKeys.lists(wsId, draftId)] as const,
};

/**
 * A draft's conversation rail (oldest-first, server-ordered). The `select`
 * layer extracts the flat array so consumers don't carry the response wrapper.
 */
export function draftMessageListOptions(wsId: string, draftId: string) {
  return queryOptions({
    queryKey: draftMessageKeys.list(wsId, draftId),
    queryFn: () => api.listDraftMessages(draftId),
    select: (data) => data.messages,
    // Only fetch once a real draft is selected (not a temp/optimistic id or an
    // empty selection).
    enabled: Boolean(draftId),
  });
}

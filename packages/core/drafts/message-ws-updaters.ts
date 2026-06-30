import type { QueryClient } from "@tanstack/react-query";
import type { DraftMessage, ListDraftMessagesResponse } from "../types";
import { draftMessageKeys } from "./message-queries";
import { isTempDraftMessageId } from "./message-mutations";

/**
 * WS updater for the draft conversation rail. Mirrors the annotation-message
 * updater: cache patching over invalidation (O(1), instant). The
 * `draft_message:created` event keeps the rail fresh across clients (and, in
 * Rail-2, when an agent posts). Per the architecture rule, WS handlers patch
 * the Query cache — never a store.
 *
 * Patches the {@link ListDraftMessagesResponse} shape under the per-draft list
 * key. A WS event for a draft whose rail isn't currently cached is a no-op (the
 * setQueriesData updater returns `old` when undefined).
 */

function patchList(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  updater: (messages: DraftMessage[]) => DraftMessage[],
): void {
  qc.setQueriesData<ListDraftMessagesResponse>(
    { queryKey: draftMessageKeys.lists(wsId, draftId) },
    (old) => {
      if (!old) return old;
      const messages = updater(old.messages);
      return { ...old, messages, total: messages.length };
    },
  );
}

export function onDraftMessageCreated(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  message: DraftMessage,
): void {
  patchList(qc, wsId, draftId, (messages) => {
    // De-dupe by id so a WS echo of our own optimistic message (already swapped
    // temp→real by the mutation's onSuccess) is a no-op.
    if (messages.some((m) => m.id === message.id)) return messages;
    // Defense for the window before onSuccess swaps temp→real: if an un-swapped
    // optimistic message with the same author+body is still in the rail, replace
    // it with the real server row rather than appending a duplicate.
    const tempMatchIndex = messages.findIndex(
      (m) =>
        isTempDraftMessageId(m.id) &&
        m.author_user_id === message.author_user_id &&
        m.body === message.body,
    );
    if (tempMatchIndex !== -1) {
      const next = messages.slice();
      next[tempMatchIndex] = message;
      return next;
    }
    return [...messages, message];
  });
}

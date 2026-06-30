import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import type { DraftMessage, ListDraftMessagesResponse } from "../types";
import { draftMessageKeys } from "./message-queries";

/**
 * Draft conversation-rail mutation with an optimistic-append flow. The cache
 * shape is the {@link ListDraftMessagesResponse} the queryFn returns (the
 * `select` that extracts `.messages` runs downstream) — a flat per-draft log,
 * simpler than the annotation thread's nested shape.
 *
 * This hook accepts `wsId` and `draftId` as parameters rather than reading them
 * from context — the rail lives inside the editor pane, which already has both
 * in scope, and parameterizing keeps the hook usable anywhere.
 */

const TEMP_MESSAGE_ID_PREFIX = "temp-draft-message-";

function makeTempId(prefix: string): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return prefix + crypto.randomUUID();
  }
  return prefix + Math.random().toString(36).slice(2);
}

export function isTempDraftMessageId(id: string): boolean {
  return id.startsWith(TEMP_MESSAGE_ID_PREFIX);
}

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

/**
 * Post a message to a draft's conversation rail with an optimistic append.
 * Inserts a temp-id placeholder immediately and swaps it for the real server
 * row on success — so the real id is in the cache before any concurrent WS
 * event (e.g. a Rail-2 agent reply) rewrites the rail. Without the swap the
 * temp message has no real id to survive a wholesale refetch and the message
 * flickers out until the next echo. Mirrors the FIXED annotation-message hook.
 */
export function useAddDraftMessage(wsId: string, draftId: string) {
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  return useMutation({
    mutationFn: (body: string) => api.addDraftMessage(draftId, body),
    onMutate: async (body) => {
      await qc.cancelQueries({ queryKey: draftMessageKeys.lists(wsId, draftId) });
      const previous = qc.getQueriesData<ListDraftMessagesResponse>({
        queryKey: draftMessageKeys.lists(wsId, draftId),
      });
      const now = new Date().toISOString();
      const tempMessageId = makeTempId(TEMP_MESSAGE_ID_PREFIX);
      const optimistic: DraftMessage = {
        id: tempMessageId,
        draft_id: draftId,
        workspace_id: wsId,
        author_type: "user",
        author_user_id: user?.id ?? "",
        body,
        created_at: now,
      };
      patchList(qc, wsId, draftId, (messages) => [...messages, optimistic]);
      return { previous, tempMessageId };
    },
    onSuccess: (message, _body, ctx) => {
      // Swap the optimistic temp message for the real server row immediately, so
      // the real id is in the cache before any concurrent WS event rewrites the
      // rail (same reasoning as the annotation-message onSuccess).
      if (!ctx?.tempMessageId) return;
      patchList(qc, wsId, draftId, (messages) =>
        messages.map((m) => (m.id === ctx.tempMessageId ? message : m)),
      );
    },
    onError: (_err, _body, ctx) => {
      ctx?.previous.forEach(([key, data]) => qc.setQueryData(key, data));
    },
    onSettled: () => {
      // Safety-net reconcile. With the temp→real swap done in onSuccess, the
      // refetch returns the same committed message, so this no longer flickers.
      qc.invalidateQueries({ queryKey: draftMessageKeys.lists(wsId, draftId) });
    },
  });
}

import type { QueryClient } from "@tanstack/react-query";
import type { DraftAnnotation, DraftAnnotationMessage, ListDraftAnnotationsResponse } from "../types";
import { draftAnnotationKeys } from "./annotation-queries";
import { isTempAnnotationId, isTempMessageId } from "./annotation-mutations";

/**
 * WS updaters for the draft annotation surface. Mirror the draft CRUD updaters:
 * cache patching over invalidation everywhere it's correct (O(1), instant).
 * WS events keep the annotation list fresh across clients (and, in slice 2,
 * when an agent authors/replies). Per the architecture rule, WS handlers patch
 * the Query cache — never a store.
 *
 * All handlers patch the {@link ListDraftAnnotationsResponse} shape under the
 * per-draft list key. A WS event for a draft whose annotations aren't currently
 * cached is a no-op (the setQueriesData updater returns `old` when undefined).
 */

function patchList(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  updater: (annotations: DraftAnnotation[]) => DraftAnnotation[],
): void {
  qc.setQueriesData<ListDraftAnnotationsResponse>(
    { queryKey: draftAnnotationKeys.lists(wsId, draftId) },
    (old) => {
      if (!old) return old;
      const annotations = updater(old.annotations);
      return { ...old, annotations, total: annotations.length };
    },
  );
}

/**
 * Replace a cached annotation with an incoming server copy while preserving any
 * in-flight optimistic (temp-id) messages the server copy doesn't yet include.
 *
 * A wholesale rewrite (the incoming `messages` are the server's authoritative
 * thread) would otherwise drop an optimistic reply that's still mid-flight —
 * exactly what happens when the reply @-mentions Aye and a concurrent
 * `draft_annotation:updated` event lands before the POST resolves. Keeping the
 * temp messages makes the reply survive; the mutation's onSuccess later swaps
 * the temp for the real row (and onDraftAnnotationMessageCreated de-dupes the
 * echo), so this never leaves a duplicate behind.
 */
function withPreservedTempMessages(
  prev: DraftAnnotation,
  incoming: DraftAnnotation,
): DraftAnnotation {
  const incomingIds = new Set(incoming.messages.map((m) => m.id));
  const survivingTempMessages = prev.messages.filter(
    (m) => isTempMessageId(m.id) && !incomingIds.has(m.id),
  );
  if (survivingTempMessages.length === 0) return incoming;
  return { ...incoming, messages: [...incoming.messages, ...survivingTempMessages] };
}

export function onDraftAnnotationCreated(
  qc: QueryClient,
  wsId: string,
  annotation: DraftAnnotation,
): void {
  patchList(qc, wsId, annotation.draft_id, (annotations) =>
    // De-dupe by id so a WS echo of our own optimistic create is a no-op. On
    // match this is a wholesale replace, so preserve any in-flight temp messages
    // the incoming copy doesn't carry (same reasoning as onDraftAnnotationUpdated).
    annotations.some((a) => a.id === annotation.id)
      ? annotations.map((a) =>
          a.id === annotation.id ? withPreservedTempMessages(a, annotation) : a,
        )
      : [...annotations, annotation],
  );
}

export function onDraftAnnotationUpdated(
  qc: QueryClient,
  wsId: string,
  annotation: DraftAnnotation,
): void {
  if (isTempAnnotationId(annotation.id)) return;
  patchList(qc, wsId, annotation.draft_id, (annotations) =>
    annotations.map((a) =>
      a.id === annotation.id ? withPreservedTempMessages(a, annotation) : a,
    ),
  );
}

export function onDraftAnnotationDeleted(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  annotationId: string,
): void {
  patchList(qc, wsId, draftId, (annotations) =>
    annotations.filter((a) => a.id !== annotationId),
  );
}

export function onDraftAnnotationMessageCreated(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  annotationId: string,
  message: DraftAnnotationMessage,
): void {
  patchList(qc, wsId, draftId, (annotations) =>
    annotations.map((a) => {
      if (a.id !== annotationId) return a;
      // De-dupe by id so a WS echo of our own optimistic reply (already swapped
      // temp→real by the mutation's onSuccess) is a no-op.
      if (a.messages.some((m) => m.id === message.id)) return a;
      // Defense for the window before onSuccess swaps temp→real: if an
      // un-swapped optimistic message with the same author+body is still in the
      // thread, replace it with the real server row rather than appending a
      // duplicate.
      const tempMatchIndex = a.messages.findIndex(
        (m) =>
          isTempMessageId(m.id) &&
          m.author_user_id === message.author_user_id &&
          m.body === message.body,
      );
      if (tempMatchIndex !== -1) {
        const messages = a.messages.slice();
        messages[tempMatchIndex] = message;
        return { ...a, messages };
      }
      return { ...a, messages: [...a.messages, message] };
    }),
  );
}

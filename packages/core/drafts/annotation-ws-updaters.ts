import type { QueryClient } from "@tanstack/react-query";
import type { DraftAnnotation, DraftAnnotationMessage, ListDraftAnnotationsResponse } from "../types";
import { draftAnnotationKeys } from "./annotation-queries";
import { isTempAnnotationId } from "./annotation-mutations";

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

export function onDraftAnnotationCreated(
  qc: QueryClient,
  wsId: string,
  annotation: DraftAnnotation,
): void {
  patchList(qc, wsId, annotation.draft_id, (annotations) =>
    // De-dupe by id so a WS echo of our own optimistic create is a no-op.
    annotations.some((a) => a.id === annotation.id)
      ? annotations.map((a) => (a.id === annotation.id ? annotation : a))
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
    annotations.map((a) => (a.id === annotation.id ? annotation : a)),
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
      // De-dupe by id so a WS echo of our own optimistic reply is a no-op.
      if (a.messages.some((m) => m.id === message.id)) return a;
      return { ...a, messages: [...a.messages, message] };
    }),
  );
}

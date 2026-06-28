import { useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import type {
  CreateDraftAnnotationRequest,
  DraftAnnotation,
  DraftAnnotationMessage,
  ListDraftAnnotationsResponse,
  UpdateDraftAnnotationRequest,
} from "../types";
import { draftAnnotationKeys } from "./annotation-queries";

/**
 * Draft-annotation mutations with optimistic-update flows. The cache shape is
 * the {@link ListDraftAnnotationsResponse} the queryFn returns (the `select`
 * that extracts `.annotations` runs downstream). All flows roll back on error
 * and invalidate at settle so the server reconciles any field the optimistic
 * patch guessed (ids, timestamps).
 *
 * These hooks accept `wsId` and `draftId` as parameters rather than reading
 * them from context — annotations live inside the editor pane, which already
 * has both in scope, and parameterizing keeps the hooks usable anywhere.
 */

const TEMP_ID_PREFIX = "temp-annotation-";

function makeTempId(prefix: string): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return prefix + crypto.randomUUID();
  }
  return prefix + Math.random().toString(36).slice(2);
}

export function isTempAnnotationId(id: string): boolean {
  return id.startsWith(TEMP_ID_PREFIX);
}

// --- Cache helpers (operate on the list-response shape) ---------------------

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

function upsertAnnotation(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  annotation: DraftAnnotation,
): void {
  patchList(qc, wsId, draftId, (annotations) => {
    if (annotations.some((a) => a.id === annotation.id)) {
      return annotations.map((a) => (a.id === annotation.id ? annotation : a));
    }
    return [...annotations, annotation];
  });
}

function replaceAnnotationId(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  tempId: string,
  annotation: DraftAnnotation,
): void {
  patchList(qc, wsId, draftId, (annotations) =>
    annotations.map((a) => (a.id === tempId ? annotation : a)),
  );
}

function removeAnnotation(
  qc: QueryClient,
  wsId: string,
  draftId: string,
  annotationId: string,
): void {
  patchList(qc, wsId, draftId, (annotations) =>
    annotations.filter((a) => a.id !== annotationId),
  );
}

// --- Hooks ------------------------------------------------------------------

/**
 * Create an annotation (optionally with an initial message). Inserts an
 * optimistic placeholder immediately, swaps for the server row on success.
 */
export function useCreateDraftAnnotation(wsId: string, draftId: string) {
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  return useMutation({
    mutationFn: (input: CreateDraftAnnotationRequest) =>
      api.createDraftAnnotation(draftId, input),
    onMutate: async (input) => {
      await qc.cancelQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
      const now = new Date().toISOString();
      const tempId = makeTempId(TEMP_ID_PREFIX);
      const messages: DraftAnnotationMessage[] =
        input.message && input.message !== ""
          ? [
              {
                id: makeTempId("temp-message-"),
                annotation_id: tempId,
                author_type: "user",
                author_user_id: user?.id ?? "",
                body: input.message,
                created_at: now,
              },
            ]
          : [];
      const optimistic: DraftAnnotation = {
        id: tempId,
        draft_id: draftId,
        workspace_id: wsId,
        author_type: "user",
        author_user_id: user?.id ?? "",
        type: input.type ?? "comment",
        quote: input.quote ?? "",
        context_before: input.context_before ?? "",
        context_after: input.context_after ?? "",
        pos_hint: input.pos_hint ?? 0,
        state: "open",
        suggestion_before: input.suggestion_before ?? null,
        suggestion_after: input.suggestion_after ?? null,
        created_at: now,
        updated_at: now,
        messages,
      };
      upsertAnnotation(qc, wsId, draftId, optimistic);
      return { tempId };
    },
    onSuccess: (annotation, _input, ctx) => {
      if (ctx?.tempId) replaceAnnotationId(qc, wsId, draftId, ctx.tempId, annotation);
      else upsertAnnotation(qc, wsId, draftId, annotation);
    },
    onError: (_err, _input, ctx) => {
      if (ctx?.tempId) removeAnnotation(qc, wsId, draftId, ctx.tempId);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
    },
  });
}

/**
 * Update an annotation — state transitions (resolve/dismiss/acknowledge) and
 * re-anchor persistence (quote/context/pos_hint moved). Optimistic in-place
 * patch. Re-anchor PATCHes are fire-and-forget from the editor's perspective,
 * so a failure rolls back silently and the next edit recomputes.
 */
export function useUpdateDraftAnnotation(wsId: string, draftId: string) {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: UpdateDraftAnnotationRequest }) =>
      api.updateDraftAnnotation(draftId, id, patch),
    onMutate: async ({ id, patch }) => {
      await qc.cancelQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
      const previous = qc.getQueriesData<ListDraftAnnotationsResponse>({
        queryKey: draftAnnotationKeys.lists(wsId, draftId),
      });
      const now = new Date().toISOString();
      patchList(qc, wsId, draftId, (annotations) =>
        annotations.map((a) => (a.id === id ? { ...a, ...patch, updated_at: now } : a)),
      );
      return { previous };
    },
    onSuccess: (annotation) => {
      upsertAnnotation(qc, wsId, draftId, annotation);
    },
    onError: (_err, _vars, ctx) => {
      ctx?.previous.forEach(([key, data]) => qc.setQueryData(key, data));
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
    },
  });
}

/** Delete an annotation with optimistic removal. */
export function useDeleteDraftAnnotation(wsId: string, draftId: string) {
  const qc = useQueryClient();

  return useMutation({
    mutationFn: (id: string) => api.deleteDraftAnnotation(draftId, id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
      const previous = qc.getQueriesData<ListDraftAnnotationsResponse>({
        queryKey: draftAnnotationKeys.lists(wsId, draftId),
      });
      removeAnnotation(qc, wsId, draftId, id);
      return { previous };
    },
    onError: (_err, _id, ctx) => {
      ctx?.previous.forEach(([key, data]) => qc.setQueryData(key, data));
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
    },
  });
}

/** Add a reply to an annotation thread with an optimistic append. */
export function useAddDraftAnnotationMessage(wsId: string, draftId: string) {
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  return useMutation({
    mutationFn: ({ annotationId, body }: { annotationId: string; body: string }) =>
      api.addDraftAnnotationMessage(draftId, annotationId, body),
    onMutate: async ({ annotationId, body }) => {
      await qc.cancelQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
      const previous = qc.getQueriesData<ListDraftAnnotationsResponse>({
        queryKey: draftAnnotationKeys.lists(wsId, draftId),
      });
      const now = new Date().toISOString();
      const optimistic: DraftAnnotationMessage = {
        id: makeTempId("temp-message-"),
        annotation_id: annotationId,
        author_type: "user",
        author_user_id: user?.id ?? "",
        body,
        created_at: now,
      };
      patchList(qc, wsId, draftId, (annotations) =>
        annotations.map((a) =>
          a.id === annotationId ? { ...a, messages: [...a.messages, optimistic] } : a,
        ),
      );
      return { previous };
    },
    onError: (_err, _vars, ctx) => {
      ctx?.previous.forEach(([key, data]) => qc.setQueryData(key, data));
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: draftAnnotationKeys.lists(wsId, draftId) });
    },
  });
}

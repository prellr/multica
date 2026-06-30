import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import { useWorkspaceId } from "../hooks";
import type { CreateDraftRequest, Draft, ListDraftsResponse, UpdateDraftRequest } from "../types";
import { draftKeys } from "./queries";
import {
  prependDraftToLists,
  removeDraftFromLists,
  replaceDraftInLists,
  swapTempDraft,
} from "./cache-helpers";

/**
 * Draft mutations with optimistic-update flows. Mirrors the task mutations:
 * the cache shape is a flat list (see cache-helpers.ts). All flows roll back on
 * error and invalidate at settle so the server's view becomes authoritative for
 * any field the optimistic patch may have guessed wrong (timestamps).
 */

/** Stable prefix for client-generated temp ids — lets the optimistic-create
 *  path identify its own placeholder rows during the swap. */
const TEMP_ID_PREFIX = "temp-draft-";

function makeTempId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return TEMP_ID_PREFIX + crypto.randomUUID();
  }
  return TEMP_ID_PREFIX + Math.random().toString(36).slice(2);
}

export function isTempDraftId(id: string): boolean {
  return id.startsWith(TEMP_ID_PREFIX);
}

/**
 * Create a draft with optimistic insertion. The placeholder row appears at the
 * top of the list immediately; on success the temp row swaps for the real one;
 * on failure it disappears (rollback). The caller drives navigation to the new
 * draft (typically pushing `?draft=<id>` once the real id lands via onSuccess).
 */
export function useCreateDraft() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);

  return useMutation({
    mutationFn: (input: CreateDraftRequest) => api.createDraft(input),
    onMutate: async (input) => {
      await qc.cancelQueries({ queryKey: draftKeys.all(wsId) });

      const now = new Date().toISOString();
      const tempId = makeTempId();
      const optimistic: Draft = {
        id: tempId,
        workspace_id: wsId,
        owner_user_id: user?.id ?? "",
        title: input.title ?? "",
        body: input.body ?? "",
        status: input.status ?? "draft",
        created_at: now,
        updated_at: now,
        // A just-created draft has no persisted Yjs state yet.
        has_yjs_state: false,
      };
      prependDraftToLists(qc, wsId, optimistic);
      return { tempId };
    },
    onSuccess: (draft, _input, ctx) => {
      if (ctx?.tempId) {
        swapTempDraft(qc, wsId, ctx.tempId, draft);
      } else {
        prependDraftToLists(qc, wsId, draft);
      }
      // Seed detail cache so opening the new draft is instant (no network).
      qc.setQueryData(draftKeys.detail(wsId, draft.id), draft);
    },
    onError: (_err, _input, ctx) => {
      if (ctx?.tempId) {
        removeDraftFromLists(qc, wsId, ctx.tempId);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: draftKeys.all(wsId) });
    },
  });
}

/**
 * Update a draft with an optimistic in-place patch. Built for autosave: a
 * debounced caller fires `{ id, patch: { body } }` (or title) on edit, the
 * optimistic patch keeps the list + detail in sync instantly, and the server
 * reconciles on settle. The patch is shallow — fields not in the request carry
 * their previous values through.
 */
export function useUpdateDraft() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: UpdateDraftRequest }) =>
      api.updateDraft(id, patch),
    onMutate: async ({ id, patch }) => {
      await qc.cancelQueries({ queryKey: draftKeys.all(wsId) });

      // draftKeys.lists() (not all()) — the latter prefix also matches detail
      // caches (a single Draft, not {drafts, total}), and the list-shape
      // updater below would crash on `old.drafts.map(...)`.
      const previousLists = qc.getQueriesData<ListDraftsResponse>({ queryKey: draftKeys.lists(wsId) });
      const previousDetail = qc.getQueryData<Draft>(draftKeys.detail(wsId, id));

      const now = new Date().toISOString();
      qc.setQueriesData<ListDraftsResponse>(
        { queryKey: draftKeys.lists(wsId) },
        (old) => {
          if (!old) return old;
          const drafts = old.drafts.map((d) =>
            d.id === id ? { ...d, ...patch, updated_at: now } : d,
          );
          return { ...old, drafts };
        },
      );
      if (previousDetail) {
        qc.setQueryData<Draft>(draftKeys.detail(wsId, id), {
          ...previousDetail,
          ...patch,
          updated_at: now,
        });
      }

      return { previousLists, previousDetail };
    },
    onSuccess: (draft) => {
      // Server-authoritative replace — picks up server-set fields (updated_at).
      replaceDraftInLists(qc, wsId, draft);
      qc.setQueryData(draftKeys.detail(wsId, draft.id), draft);
    },
    onError: (_err, vars, ctx) => {
      ctx?.previousLists.forEach(([key, data]) => {
        qc.setQueryData(key, data);
      });
      if (ctx?.previousDetail) {
        qc.setQueryData(draftKeys.detail(wsId, vars.id), ctx.previousDetail);
      }
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: draftKeys.detail(wsId, vars.id) });
    },
  });
}

/**
 * Delete a draft with an optimistic remove. The row disappears from every list
 * cache immediately; on error the snapshots are restored. Detail cache is
 * dropped on success (refetching a deleted draft would 404).
 */
export function useDeleteDraft() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (id: string) => api.deleteDraft(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: draftKeys.all(wsId) });
      const previousLists = qc.getQueriesData<ListDraftsResponse>({
        queryKey: draftKeys.lists(wsId),
      });
      const previousDetail = qc.getQueryData<Draft>(draftKeys.detail(wsId, id));
      removeDraftFromLists(qc, wsId, id);
      return { id, previousLists, previousDetail };
    },
    onSuccess: (_data, id) => {
      qc.removeQueries({ queryKey: draftKeys.detail(wsId, id) });
    },
    onError: (_err, _id, ctx) => {
      ctx?.previousLists.forEach(([key, data]) => {
        qc.setQueryData(key, data);
      });
      if (ctx?.previousDetail && ctx.id) {
        qc.setQueryData(draftKeys.detail(wsId, ctx.id), ctx.previousDetail);
      }
    },
  });
}

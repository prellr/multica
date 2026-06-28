import type { QueryClient } from "@tanstack/react-query";
import type { Draft, ListDraftsResponse } from "../types";
import { draftKeys } from "./queries";

/**
 * Drafts live in a flat, newest-edited-first list, so cache patching is array
 * splicing. All helpers operate on the {@link ListDraftsResponse} shape that
 * {@link draftListOptions}'s queryFn returns — the `select` projection that
 * extracts `.drafts` is applied downstream (TanStack re-runs select after every
 * cache write).
 *
 * Helpers iterate `qc.setQueriesData` over the `lists` prefix so any mounted
 * list cache stays consistent.
 */

export function prependDraftToLists(qc: QueryClient, wsId: string, draft: Draft): void {
  qc.setQueriesData<ListDraftsResponse>(
    { queryKey: draftKeys.lists(wsId) },
    (old) => {
      if (!old) return old;
      // Defensive: if the same id is already present (WS event landed first),
      // don't double-insert.
      if (old.drafts.some((d) => d.id === draft.id)) return old;
      return { ...old, drafts: [draft, ...old.drafts], total: old.total + 1 };
    },
  );
}

export function replaceDraftInLists(qc: QueryClient, wsId: string, draft: Draft): void {
  qc.setQueriesData<ListDraftsResponse>(
    { queryKey: draftKeys.lists(wsId) },
    (old) => {
      if (!old) return old;
      let touched = false;
      const drafts = old.drafts.map((d) => {
        if (d.id !== draft.id) return d;
        touched = true;
        return draft;
      });
      // No-op write if id wasn't in this cache — preserves referential
      // identity, keeps React from re-rendering subscribers unnecessarily.
      return touched ? { ...old, drafts } : old;
    },
  );
}

export function removeDraftFromLists(qc: QueryClient, wsId: string, draftId: string): void {
  qc.setQueriesData<ListDraftsResponse>(
    { queryKey: draftKeys.lists(wsId) },
    (old) => {
      if (!old) return old;
      const before = old.drafts.length;
      const drafts = old.drafts.filter((d) => d.id !== draftId);
      if (drafts.length === before) return old;
      return { ...old, drafts, total: Math.max(0, old.total - 1) };
    },
  );
}

/**
 * Replace a placeholder (temp) draft with the server-confirmed one. Used by the
 * optimistic create flow: insert with a temp id while the request is in flight,
 * then swap when the real row comes back.
 */
export function swapTempDraft(qc: QueryClient, wsId: string, tempId: string, draft: Draft): void {
  qc.setQueriesData<ListDraftsResponse>(
    { queryKey: draftKeys.lists(wsId) },
    (old) => {
      if (!old) return old;
      const drafts = old.drafts.map((d) => (d.id === tempId ? draft : d));
      return { ...old, drafts };
    },
  );
}

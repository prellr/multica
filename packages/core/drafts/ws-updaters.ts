import type { QueryClient } from "@tanstack/react-query";
import type { Draft } from "../types";
import { draftKeys } from "./queries";
import {
  prependDraftToLists,
  removeDraftFromLists,
  replaceDraftInLists,
} from "./cache-helpers";
import { isTempDraftId } from "./mutations";

/**
 * WS updaters for the draft surface. Handler signatures mirror the task
 * equivalents (qc, wsId, payload) so the realtime sync layer wires them with
 * the same pattern. Cache patching is preferred over invalidation everywhere
 * it's correct: patching is O(1) and renders instantly. WS events keep the
 * draft list fresh across clients (and, in a later slice, when the agent edits
 * a draft) — they never write to a store directly.
 */

export function onDraftCreated(qc: QueryClient, wsId: string, draft: Draft): void {
  // The cache helper de-dupes by id, so a duplicate WS event after our own
  // optimistic insert is a no-op.
  prependDraftToLists(qc, wsId, draft);
  qc.setQueryData(draftKeys.detail(wsId, draft.id), draft);
}

export function onDraftUpdated(qc: QueryClient, wsId: string, draft: Draft): void {
  // A WS update for a temp id can never happen (the server doesn't know temp
  // ids); the guard makes the contract explicit.
  if (isTempDraftId(draft.id)) return;
  replaceDraftInLists(qc, wsId, draft);
  qc.setQueryData<Draft>(draftKeys.detail(wsId, draft.id), (old) => (old ? { ...old, ...draft } : draft));
}

export function onDraftDeleted(qc: QueryClient, wsId: string, draftId: string): void {
  removeDraftFromLists(qc, wsId, draftId);
  qc.removeQueries({ queryKey: draftKeys.detail(wsId, draftId) });
}

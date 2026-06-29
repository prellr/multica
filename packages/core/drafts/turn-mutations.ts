import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { DraftTurnResponse, TaskMessagePayload } from "../types";

/**
 * Drafts slice 2 — the Send-turn. The human clicks Send; we enqueue one
 * conservative turn for Aye and get back the task id. Aye's per-action moves
 * (thread replies, suggestions) arrive live as draft_annotation:* WS events the
 * annotation cache already consumes; her thinking/tool stream arrives as
 * task:message events the global realtime sync writes into the task-messages
 * cache. This module is the thin glue: start a turn, and read that task's
 * message stream for the narration rail.
 *
 * The "which turn is active for this draft" state is intentionally NOT a store —
 * it's transient UI state owned by the editor component (one active turn per
 * open draft), so the component holds the returned task_id in local state and
 * passes it to useDraftTurnMessages.
 */

/**
 * Start a draft turn. Not optimistic — there's nothing to roll back; the turn
 * either enqueues (202 + task_id) or the server reports why it can't (409, e.g.
 * no daemon connected). The caller reads the returned task_id and subscribes to
 * its narration.
 */
export function useStartDraftTurn(draftId: string) {
  return useMutation<DraftTurnResponse, Error, void>({
    mutationFn: () => api.startDraftTurn(draftId),
  });
}

/**
 * Read the live task:message stream for an in-flight turn. The global realtime
 * sync writes task:message events into ["task-messages", taskId] as they
 * arrive; this hook surfaces that cache as the narration source. Returns [] for
 * an empty/absent taskId so the rail renders nothing before a turn starts.
 *
 * The query has no queryFn (the cache is populated by WS, not a fetch) — it's a
 * read-only window onto the WS-maintained cache, matching how chat consumes the
 * same key.
 */
export function useDraftTurnMessages(taskId: string | null | undefined): TaskMessagePayload[] {
  const qc = useQueryClient();
  const { data } = useQuery<TaskMessagePayload[]>({
    queryKey: ["task-messages", taskId ?? ""],
    queryFn: () => qc.getQueryData<TaskMessagePayload[]>(["task-messages", taskId ?? ""]) ?? [],
    enabled: !!taskId,
    // No staleness/refetch: the WS handler owns this cache. We just re-render
    // when setQueryData fires.
    staleTime: Infinity,
    gcTime: 0,
  });
  return data ?? [];
}

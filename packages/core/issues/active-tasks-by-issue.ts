import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { agentTaskSnapshotKeys } from "../agents/queries";
import type { AgentTask } from "../types/agent";

// Active = a task that hasn't reached a terminal state yet. Mirrors the
// detail-page `ListActiveTasksByIssue` SQL definition so the board badge
// matches the issue-detail banner.
const ACTIVE = new Set<string>(["queued", "dispatched", "running"]);

// Stay-in-sync surface with `agentTaskSnapshotOptions(wsId)`: same queryKey
// + same queryFn so TanStack Query's cache is shared (one fetch, multiple
// projected views) and any future tweak there is mirrored here. The
// staleTime/gcTime defaults are inherited from the global QueryClient,
// matching the lifetime of the underlying snapshot.
function snapshotBase(wsId: string) {
  return {
    queryKey: agentTaskSnapshotKeys.list(wsId),
    queryFn: () => api.getAgentTaskSnapshot(),
    staleTime: 30 * 1000,
    gcTime: 5 * 60 * 1000,
    refetchOnWindowFocus: true,
  } as const;
}

/**
 * Derived map: issue id → list of active agent tasks on that issue.
 *
 * Shares the workspace-wide `agentTaskSnapshot` cache key so the snapshot
 * is fetched once and shaped into multiple views; WS `task:*` events that
 * invalidate the snapshot automatically refresh this selector.
 */
export function activeTasksByIssueOptions(wsId: string) {
  return queryOptions({
    ...snapshotBase(wsId),
    select: (tasks: AgentTask[]) => {
      const map = new Map<string, AgentTask[]>();
      for (const t of tasks) {
        if (!t.issue_id) continue;
        if (!ACTIVE.has(t.status)) continue;
        const arr = map.get(t.issue_id);
        if (arr) arr.push(t);
        else map.set(t.issue_id, [t]);
      }
      return map;
    },
  });
}

/**
 * Set of issue ids that currently have at least one active agent task.
 * Used by the Working filter for O(1) membership checks.
 */
export function workingIssueIdsOptions(wsId: string) {
  return queryOptions({
    ...snapshotBase(wsId),
    select: (tasks: AgentTask[]) => {
      const ids = new Set<string>();
      for (const t of tasks) {
        if (!t.issue_id) continue;
        if (!ACTIVE.has(t.status)) continue;
        ids.add(t.issue_id);
      }
      return ids;
    },
  });
}

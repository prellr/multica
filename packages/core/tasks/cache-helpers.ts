import type { QueryClient } from "@tanstack/react-query";
import type { ListTasksParams, ListTasksResponse, Task } from "../types";
import { taskKeys } from "./queries";

/**
 * Tasks live in a flat list (no per-status buckets like issues), so cache
 * patching is just array splicing. All helpers here operate on the
 * {@link ListTasksResponse} shape that {@link taskListOptions}'s queryFn
 * returns — the `select` projection that extracts `.tasks` is applied
 * downstream and doesn't need to be re-derived here (TanStack re-runs
 * select after every cache write).
 *
 * The helpers iterate `qc.getQueriesData` rather than targeting one key
 * because multiple filter combinations may have their own cached lists
 * mounted at once (e.g. the workspace tasks page and a future "my open
 * tasks" page). Each helper patches every matching cache so the UI stays
 * consistent regardless of which list the user is viewing.
 */

export function prependTaskToLists(qc: QueryClient, wsId: string, task: Task): void {
  qc.setQueriesData<ListTasksResponse>(
    { queryKey: taskKeys.all(wsId) },
    (old) => {
      if (!old) return old;
      // Defensive: if the same id is already present (WS event landed first),
      // don't double-insert.
      if (old.tasks.some((t) => t.id === task.id)) return old;
      return { ...old, tasks: [task, ...old.tasks], total: old.total + 1 };
    },
  );
}

export function replaceTaskInLists(qc: QueryClient, wsId: string, task: Task): void {
  qc.setQueriesData<ListTasksResponse>(
    { queryKey: taskKeys.all(wsId) },
    (old) => {
      if (!old) return old;
      let touched = false;
      const tasks = old.tasks.map((t) => {
        if (t.id !== task.id) return t;
        touched = true;
        return task;
      });
      // No-op write if id wasn't in this cache — preserves referential
      // identity, keeps React from re-rendering subscribers unnecessarily.
      return touched ? { ...old, tasks } : old;
    },
  );
}

export function removeTaskFromLists(qc: QueryClient, wsId: string, taskId: string): void {
  qc.setQueriesData<ListTasksResponse>(
    { queryKey: taskKeys.all(wsId) },
    (old) => {
      if (!old) return old;
      const before = old.tasks.length;
      const tasks = old.tasks.filter((t) => t.id !== taskId);
      if (tasks.length === before) return old;
      return { ...old, tasks, total: Math.max(0, old.total - 1) };
    },
  );
}

/**
 * Replace a placeholder (temp) task with the server-confirmed one.
 * Used by the optimistic create flow: we insert with a temp id while the
 * request is in flight, then swap when the real row comes back.
 */
export function swapTempTask(qc: QueryClient, wsId: string, tempId: string, task: Task): void {
  qc.setQueriesData<ListTasksResponse>(
    { queryKey: taskKeys.all(wsId) },
    (old) => {
      if (!old) return old;
      const tasks = old.tasks.map((t) => (t.id === tempId ? task : t));
      return { ...old, tasks };
    },
  );
}

// Filter helper kept here even though no callsite uses it yet — when PR 4+1
// adds the "My Tasks" assignee-filtered view it'll need to know whether an
// optimistically-created task matches the filter before deciding to prepend.
// Leaving it would-be-tested only with that surface, so for now this is
// a one-off helper local to the mutation flow.
export function taskMatchesFilter(task: Task, filter: ListTasksParams): boolean {
  if (filter.status !== undefined && task.status !== filter.status) return false;
  if (filter.assignee_id !== undefined && task.assignee_id !== filter.assignee_id) return false;
  if (filter.creator_id !== undefined && task.creator_id !== filter.creator_id) return false;
  if (filter.project_id !== undefined && task.project_id !== filter.project_id) return false;
  if (filter.parent_issue_id !== undefined && task.parent_issue_id !== filter.parent_issue_id)
    return false;
  return true;
}

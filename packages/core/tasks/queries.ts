import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { ListTasksParams } from "../types";

/**
 * Task query keys. Mirrors the shape of {@link issueKeys} but lives in its
 * own namespace so issue and task caches never collide. Workspace scoping
 * is part of the key so switching workspaces automatically swaps the
 * visible task list with no manual invalidation.
 */
export const taskKeys = {
  all: (wsId: string) => ["tasks", wsId] as const,
  list: (wsId: string, filter: ListTasksParams) =>
    [...taskKeys.all(wsId), "list", filter] as const,
  detail: (wsId: string, id: string) =>
    [...taskKeys.all(wsId), "detail", id] as const,
};

/** Page size for the task list. Smaller than the issue list since tasks
 *  are a flat list (no per-status pagination) and tend to be shorter-lived
 *  — most users won't accumulate hundreds of open tasks at a time. */
export const TASK_PAGE_SIZE = 100;

/**
 * Workspace-scoped task list. Unlike {@link issueListOptions} this is a
 * single flat query — tasks have a simpler lifecycle (no backlog state,
 * no in_review state) so per-status buckets would just be overhead.
 * Filtering is server-side via {@link ListTasksParams}.
 *
 * The `filter` object is baked into the queryKey so distinct filter
 * combinations get their own cache entries — switching from "all tasks"
 * to "my open tasks" is instant after first load.
 */
export function taskListOptions(wsId: string, filter: ListTasksParams = {}) {
  return queryOptions({
    queryKey: taskKeys.list(wsId, filter),
    queryFn: () => api.listTasks({ workspace_id: wsId, limit: TASK_PAGE_SIZE, ...filter }),
    // The select layer extracts the array so consumers don't have to remember
    // the response wrapper shape. Total stays available via a separate
    // queryOptions/select if needed.
    select: (data) => data.tasks,
  });
}

export function taskDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: taskKeys.detail(wsId, id),
    queryFn: () => api.getTask(id),
  });
}

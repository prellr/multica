import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useAuthStore } from "../auth";
import { useWorkspaceId } from "../hooks";
import { issueKeys } from "../issues/queries";
import { onIssueCreated } from "../issues/ws-updaters";
import type { CreateTaskRequest, Issue, ListTasksResponse, Task, UpdateTaskRequest } from "../types";
import { taskKeys } from "./queries";
import {
  prependTaskToLists,
  removeTaskFromLists,
  replaceTaskInLists,
  swapTempTask,
} from "./cache-helpers";

/**
 * Task mutations with optimistic-update flows. Unlike the issues equivalent,
 * the cache shape is a flat list — see cache-helpers.ts for the patching
 * primitives. All flows roll back on error and invalidate at settle so the
 * server's view becomes authoritative for any field the optimistic patch
 * may have guessed wrong (timestamps, position, server-set defaults).
 */

/** Stable prefix for client-generated temp ids. Lets the optimistic-create
 *  path identify its own placeholder rows during the swap, and gives the
 *  WS layer something to filter on if a stray event for a temp id ever
 *  arrives (it won't, but defense-in-depth). */
const TEMP_ID_PREFIX = "temp-task-";

function makeTempId(): string {
  // The id needs to be unique within the cache for the duration of the
  // request. `crypto.randomUUID` is available everywhere we care about
  // (jsdom in tests, modern browsers). Fall back to Math.random for the
  // unlikely older runtime where it isn't.
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return TEMP_ID_PREFIX + crypto.randomUUID();
  }
  return TEMP_ID_PREFIX + Math.random().toString(36).slice(2);
}

export function isTempTaskId(id: string): boolean {
  return id.startsWith(TEMP_ID_PREFIX);
}

/**
 * Create a task with optimistic insertion. The placeholder row appears at
 * the top of the list immediately; the input clears; the network round-trip
 * happens in the background. On success the temp row swaps for the real
 * one; on failure it disappears (rollback). Errors should be surfaced by
 * the caller via the returned mutation's `error` field — this hook stays
 * UI-free so it's reusable from other create flows (PR 5's promote-to-
 * issue flow may want to reuse the create primitive).
 */
export function useCreateTask() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const user = useAuthStore((s) => s.user);

  return useMutation({
    mutationFn: (input: CreateTaskRequest) => api.createTask(input),
    onMutate: async (input) => {
      // Cancel in-flight task list fetches so they don't overwrite our
      // optimistic patch when they land mid-flight.
      await qc.cancelQueries({ queryKey: taskKeys.all(wsId) });

      const now = new Date().toISOString();
      const tempId = makeTempId();
      const optimistic: Task = {
        id: tempId,
        workspace_id: wsId,
        kind: "task",
        title: input.title,
        description: input.description ?? null,
        status: input.status ?? "todo",
        assignee_type: input.assignee_type ?? null,
        assignee_id: input.assignee_id ?? null,
        // Without a logged-in user this hook can't be called from a UI that
        // requires auth — but a defensive empty string is safer than asserting.
        creator_type: "member",
        creator_id: user?.id ?? "",
        parent_issue_id: input.parent_issue_id ?? null,
        project_id: input.project_id ?? null,
        position: 0,
        start_date: input.start_date ?? null,
        due_date: input.due_date ?? null,
        metadata: {},
        created_at: now,
        updated_at: now,
      };
      prependTaskToLists(qc, wsId, optimistic);
      return { tempId };
    },
    onSuccess: (task, _input, ctx) => {
      if (ctx?.tempId) {
        swapTempTask(qc, wsId, ctx.tempId, task);
      } else {
        // Unexpected — onMutate always sets tempId. Still safe to converge.
        prependTaskToLists(qc, wsId, task);
      }
      // Seed detail cache so a click-through to the new task is instant.
      qc.setQueryData(taskKeys.detail(wsId, task.id), task);
    },
    onError: (_err, _input, ctx) => {
      if (ctx?.tempId) {
        removeTaskFromLists(qc, wsId, ctx.tempId);
      }
    },
    onSettled: () => {
      // The optimistic row is correct shape-wise but may differ from the
      // server's view on fields we didn't simulate (server-set position,
      // workspace defaults, etc.). Background-revalidate to converge.
      qc.invalidateQueries({ queryKey: taskKeys.all(wsId) });
    },
  });
}

/**
 * Update a task with an optimistic in-place patch. Useful for both the
 * status-toggle on TaskRow and (eventually) inline title edits. The patch
 * is shallow — for fields that aren't in the request, the previous values
 * carry through.
 */
export function useUpdateTask() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: UpdateTaskRequest }) =>
      api.updateTask(id, patch),
    onMutate: async ({ id, patch }) => {
      await qc.cancelQueries({ queryKey: taskKeys.all(wsId) });

      // Snapshot every list cache that contains this task so we can roll
      // back if the request fails. Detail cache is snapshotted separately
      // because it has its own queryKey.
      //
      // taskKeys.lists() rather than taskKeys.all() is critical here —
      // the latter prefix also matches detail caches (a single Task,
      // not {tasks, total}), and the list-shape updater below would
      // crash on `old.tasks.map(...)` because `tasks` is undefined.
      const previousLists = qc.getQueriesData<ListTasksResponse>({ queryKey: taskKeys.lists(wsId) });
      const previousDetail = qc.getQueryData<Task>(taskKeys.detail(wsId, id));

      // Patch every list cache that contains this task.
      qc.setQueriesData<ListTasksResponse>(
        { queryKey: taskKeys.lists(wsId) },
        (old) => {
          if (!old) return old;
          const tasks = old.tasks.map((t) =>
            t.id === id ? { ...t, ...patch, updated_at: new Date().toISOString() } : t,
          );
          return { ...old, tasks };
        },
      );
      // Patch the detail cache if present.
      if (previousDetail) {
        qc.setQueryData<Task>(taskKeys.detail(wsId, id), {
          ...previousDetail,
          ...patch,
          updated_at: new Date().toISOString(),
        });
      }

      return { previousLists, previousDetail };
    },
    onSuccess: (task) => {
      // Server-authoritative replace — picks up server-set fields we couldn't
      // simulate optimistically.
      replaceTaskInLists(qc, wsId, task);
      qc.setQueryData(taskKeys.detail(wsId, task.id), task);
    },
    onError: (_err, vars, ctx) => {
      // Restore every snapshotted cache.
      ctx?.previousLists.forEach(([key, data]) => {
        qc.setQueryData(key, data);
      });
      if (ctx?.previousDetail) {
        qc.setQueryData(taskKeys.detail(wsId, vars.id), ctx.previousDetail);
      }
    },
    onSettled: (_data, _err, vars) => {
      qc.invalidateQueries({ queryKey: taskKeys.detail(wsId, vars.id) });
    },
  });
}

/**
 * Delete a task with an optimistic remove. The row disappears from every
 * list cache immediately; on error the snapshots are restored so the row
 * comes back. Detail cache is dropped on success (refetching a deleted
 * task would 404; better to evict than serve stale).
 *
 * No undo path in this hook — the surface that triggers delete is
 * responsible for any "Undo" toast affordance if it wants one. The
 * mutation rejects with the network/HTTP error if the request fails
 * so the UI can surface it; the rollback happens automatically.
 */
export function useDeleteTask() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (id: string) => api.deleteTask(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: taskKeys.all(wsId) });
      // See useUpdateTask onMutate for the lists() vs all() rationale.
      const previousLists = qc.getQueriesData<ListTasksResponse>({
        queryKey: taskKeys.lists(wsId),
      });
      const previousDetail = qc.getQueryData<Task>(taskKeys.detail(wsId, id));
      removeTaskFromLists(qc, wsId, id);
      return { id, previousLists, previousDetail };
    },
    onSuccess: (_data, id) => {
      // Forget the detail cache — refetching after delete would 404, and
      // any subscriber on /tasks/:id should resolve to "not found" via
      // the loader path (404 → toast or redirect, owned by the caller).
      qc.removeQueries({ queryKey: taskKeys.detail(wsId, id) });
    },
    onError: (_err, _id, ctx) => {
      ctx?.previousLists.forEach(([key, data]) => {
        qc.setQueryData(key, data);
      });
      if (ctx?.previousDetail && ctx.id) {
        qc.setQueryData(taskKeys.detail(wsId, ctx.id), ctx.previousDetail);
      }
    },
  });
}

/**
 * Promote a task into a full issue. The task disappears from the task
 * surface and the resulting issue appears in the issue surface. The id
 * is preserved across the transition (server reuses the row), so a
 * frontend that holds a reference doesn't need to re-resolve.
 *
 * Optimistic flow:
 *   - Remove the row from every task list cache immediately.
 *   - DON'T optimistically add to the issue cache — the server assigns
 *     the workspace-scoped number + identifier, and we'd have to guess
 *     them. A blank-number row in the issue cache would render with a
 *     broken identifier link until the request lands.
 *   - On success: seed the issue caches (list + detail) via the existing
 *     onIssueCreated helper so it slots in to the right status bucket.
 *   - On error: restore the task list snapshots so the row reappears.
 *
 * The caller is responsible for navigation (typically: after success,
 * push("/{slug}/issues/:id")). Keeping nav out of the hook lets the
 * promote affordance ship from multiple surfaces (row context menu,
 * detail page button) with different post-success behavior.
 */
export function usePromoteTask() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();

  return useMutation({
    mutationFn: (id: string) => api.promoteTask(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: taskKeys.all(wsId) });
      // See useUpdateTask onMutate for the lists() vs all() rationale.
      const previousLists = qc.getQueriesData<ListTasksResponse>({
        queryKey: taskKeys.lists(wsId),
      });
      // Snapshot the task detail too — on error we restore it so the
      // /tasks/:id route the user came from still renders while they
      // retry. removeTaskFromLists doesn't touch the detail cache.
      const previousDetail = qc.getQueryData<Task>(taskKeys.detail(wsId, id));

      removeTaskFromLists(qc, wsId, id);
      return { id, previousLists, previousDetail };
    },
    onSuccess: (issue, _id, _ctx) => {
      // Seed the issue caches. onIssueCreated handles bucket placement,
      // child-progress recomputation, and the my-issues fan-out — same
      // path a fresh issue:created WS event takes.
      onIssueCreated(qc, wsId, issue);
      qc.setQueryData<Issue>(issueKeys.detail(wsId, issue.id), issue);
      // The task detail cache for this id is now stale — the same id is
      // now an issue. Remove rather than refetch (refetch would 404).
      qc.removeQueries({ queryKey: taskKeys.detail(wsId, issue.id) });
    },
    onError: (_err, _id, ctx) => {
      ctx?.previousLists.forEach(([key, data]) => {
        qc.setQueryData(key, data);
      });
      if (ctx?.previousDetail && ctx.id) {
        qc.setQueryData(taskKeys.detail(wsId, ctx.id), ctx.previousDetail);
      }
    },
  });
}

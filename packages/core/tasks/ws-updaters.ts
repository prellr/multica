import type { QueryClient } from "@tanstack/react-query";
import type { Task } from "../types";
import { taskKeys } from "./queries";
import {
  prependTaskToLists,
  removeTaskFromLists,
  replaceTaskInLists,
} from "./cache-helpers";
import { isTempTaskId } from "./mutations";

/**
 * WS updaters for the task surface. The handler signatures mirror the issue
 * equivalents in shape (qc, wsId, payload) so the realtime sync layer wires
 * them with the same pattern. Unlike the issue updaters these are small —
 * tasks have a flat cache and no bucket geometry to maintain.
 *
 * Cache patching is preferred over invalidation everywhere it's correct:
 * patching is O(1) and gives the user an instant render; invalidation
 * forces a network round-trip. The trade-off is that any field the server
 * sets after the patch (server-assigned timestamps, position) will be
 * slightly stale until the next list fetch. For tasks this is acceptable —
 * the surface is human-paced; the cache will be re-fetched on any other
 * action (sidebar nav, tab switch, etc).
 */

export function onUserTaskCreated(qc: QueryClient, wsId: string, task: Task): void {
  // Defense in depth: the cache helper already de-dupes by id, so a duplicate
  // WS event after our own optimistic insert is a no-op.
  prependTaskToLists(qc, wsId, task);
  // Seed detail cache so a click-through from the WS-driven row insert is
  // instant (no network).
  qc.setQueryData(taskKeys.detail(wsId, task.id), task);
}

export function onUserTaskUpdated(qc: QueryClient, wsId: string, task: Task): void {
  // Replace in every list cache + the detail. If our own optimistic create
  // is still in flight, the temp row won't match by id so this is a no-op
  // for the placeholder — once the create resolves, swapTempTask handles
  // the temp → real transition. WS update for a temp id can never happen
  // (the server doesn't know the temp id) but the guard makes the contract
  // explicit.
  if (isTempTaskId(task.id)) return;
  replaceTaskInLists(qc, wsId, task);
  qc.setQueryData<Task>(taskKeys.detail(wsId, task.id), (old) => (old ? { ...old, ...task } : task));
}

export function onUserTaskDeleted(qc: QueryClient, wsId: string, taskId: string): void {
  removeTaskFromLists(qc, wsId, taskId);
  qc.removeQueries({ queryKey: taskKeys.detail(wsId, taskId) });
}

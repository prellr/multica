import { describe, expect, it, beforeEach } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import {
  prependTaskToLists,
  removeTaskFromLists,
  replaceTaskInLists,
  swapTempTask,
  taskMatchesFilter,
} from "./cache-helpers";
import { taskKeys } from "./queries";
import type { ListTasksResponse, Task } from "../types";

const WS_ID = "ws-1";

// Minimal task factory — only the fields the cache helpers care about
// are interesting; everything else has plausible defaults so the test
// stays focused on the patching behavior.
function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: "task-1",
    workspace_id: WS_ID,
    kind: "task",
    title: "test",
    description: null,
    status: "todo",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position: 0,
    start_date: null,
    due_date: null,
    metadata: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function seed(qc: QueryClient, tasks: Task[]): void {
  // Seed the default-filter cache the way taskListOptions(wsId, {}) would.
  const response: ListTasksResponse = { tasks, total: tasks.length };
  qc.setQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}), response);
}

describe("task cache helpers", () => {
  let qc: QueryClient;

  beforeEach(() => {
    qc = new QueryClient({
      // Tests aren't trying to exercise real fetches — disable retries so
      // any accidental refetch surfaces fast.
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
  });

  describe("prependTaskToLists", () => {
    it("inserts at the front and bumps total", () => {
      seed(qc, [makeTask({ id: "a" })]);
      prependTaskToLists(qc, WS_ID, makeTask({ id: "b" }));
      const cached = qc.getQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}));
      expect(cached?.tasks.map((t) => t.id)).toEqual(["b", "a"]);
      expect(cached?.total).toBe(2);
    });

    it("is a no-op when the id is already present", () => {
      // Defends against the WS event landing after our own optimistic
      // create — without this guard the row would appear twice.
      seed(qc, [makeTask({ id: "a" })]);
      prependTaskToLists(qc, WS_ID, makeTask({ id: "a" }));
      const cached = qc.getQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}));
      expect(cached?.tasks).toHaveLength(1);
      expect(cached?.total).toBe(1);
    });
  });

  describe("replaceTaskInLists", () => {
    it("patches the matching row in place", () => {
      seed(qc, [makeTask({ id: "a", title: "old" })]);
      replaceTaskInLists(qc, WS_ID, makeTask({ id: "a", title: "new" }));
      const cached = qc.getQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}));
      expect(cached?.tasks[0]?.title).toBe("new");
    });

    it("preserves object identity when no row matches", () => {
      // Important: TanStack subscribers re-render on every cache write, so
      // a no-op replace MUST return the same object reference. Otherwise
      // mutations on unrelated tasks cause unrelated lists to re-render.
      const initial: ListTasksResponse = { tasks: [makeTask({ id: "a" })], total: 1 };
      qc.setQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}), initial);
      replaceTaskInLists(qc, WS_ID, makeTask({ id: "not-in-cache" }));
      const after = qc.getQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}));
      expect(after).toBe(initial);
    });
  });

  describe("removeTaskFromLists", () => {
    it("removes the matching row and decrements total", () => {
      seed(qc, [makeTask({ id: "a" }), makeTask({ id: "b" })]);
      removeTaskFromLists(qc, WS_ID, "a");
      const cached = qc.getQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}));
      expect(cached?.tasks.map((t) => t.id)).toEqual(["b"]);
      expect(cached?.total).toBe(1);
    });

    it("clamps total at zero when the cache is somehow out of sync", () => {
      // Defensive: if total was already 0 (or the row appears in tasks
      // without total reflecting it), don't go negative. A negative total
      // would render as "-1 tasks" downstream.
      qc.setQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}), {
        tasks: [makeTask({ id: "a" })],
        total: 0,
      });
      removeTaskFromLists(qc, WS_ID, "a");
      const cached = qc.getQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}));
      expect(cached?.total).toBe(0);
    });
  });

  describe("swapTempTask", () => {
    it("replaces the temp row with the real row at the same position", () => {
      seed(qc, [
        makeTask({ id: "temp-task-xyz", title: "in flight" }),
        makeTask({ id: "real-a" }),
      ]);
      swapTempTask(qc, WS_ID, "temp-task-xyz", makeTask({ id: "real-b", title: "confirmed" }));
      const cached = qc.getQueryData<ListTasksResponse>(taskKeys.list(WS_ID, {}));
      expect(cached?.tasks.map((t) => t.id)).toEqual(["real-b", "real-a"]);
      expect(cached?.tasks[0]?.title).toBe("confirmed");
    });
  });

  describe("taskMatchesFilter", () => {
    it("matches when no filter fields are set", () => {
      expect(taskMatchesFilter(makeTask(), {})).toBe(true);
    });

    it("respects status, assignee, creator, project, and parent filters", () => {
      const task = makeTask({
        status: "in_progress",
        assignee_id: "user-1",
        creator_id: "user-2",
        project_id: "proj-1",
        parent_issue_id: "parent-1",
      });
      expect(taskMatchesFilter(task, { status: "in_progress" })).toBe(true);
      expect(taskMatchesFilter(task, { status: "done" })).toBe(false);
      expect(taskMatchesFilter(task, { assignee_id: "user-1" })).toBe(true);
      expect(taskMatchesFilter(task, { assignee_id: "user-9" })).toBe(false);
      expect(taskMatchesFilter(task, { creator_id: "user-2" })).toBe(true);
      expect(taskMatchesFilter(task, { project_id: "proj-1" })).toBe(true);
      expect(taskMatchesFilter(task, { parent_issue_id: "parent-1" })).toBe(true);
    });
  });
});

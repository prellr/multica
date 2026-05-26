import { describe, expect, it } from "vitest";
import type { Task } from "@multica/core/types";
import { wouldCreateCycle } from "./task-tree";

function makeTask(id: string, parent_issue_id: string | null = null): Task {
  return {
    id,
    workspace_id: "ws-1",
    kind: "task",
    title: id,
    description: null,
    status: "todo",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "u-1",
    parent_issue_id,
    project_id: null,
    position: 0,
    start_date: null,
    due_date: null,
    metadata: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

describe("wouldCreateCycle", () => {
  // Tree:  A
  //        ├── B
  //        │   └── C
  //        └── D
  const tasks = [
    makeTask("A", null),
    makeTask("B", "A"),
    makeTask("C", "B"),
    makeTask("D", "A"),
  ];

  it("blocks dropping a task onto itself", () => {
    expect(wouldCreateCycle("A", "A", tasks)).toBe(true);
  });

  it("blocks dropping a task onto its own child", () => {
    expect(wouldCreateCycle("A", "B", tasks)).toBe(true);
  });

  it("blocks dropping a task onto its own grandchild", () => {
    // A -> B -> C; reparenting A onto C would make A a descendant of itself
    expect(wouldCreateCycle("A", "C", tasks)).toBe(true);
  });

  it("allows dropping a task onto a sibling", () => {
    expect(wouldCreateCycle("B", "D", tasks)).toBe(false);
  });

  it("allows dropping a leaf onto an unrelated branch's leaf", () => {
    // D is sibling of B (under A); C is child of B. Dropping D under C
    // is fine — D is not in C's ancestry.
    expect(wouldCreateCycle("D", "C", tasks)).toBe(false);
  });

  it("conservatively allows when target's chain leaves the visible set", () => {
    // E's parent is X, which isn't in the visible set. The walk runs
    // off the map at X; we allow the drop and let the server be the
    // authoritative check.
    const partial = [makeTask("dragged", null), makeTask("E", "X")];
    expect(wouldCreateCycle("dragged", "E", partial)).toBe(false);
  });

  it("blocks when walking hits maxDepth (defends against pre-existing cycles)", () => {
    // Construct A -> B -> A (broken data). Walking from A's "parent" B
    // would loop forever; the depth guard kicks in and returns true so
    // we refuse to deepen the cycle.
    const cyclic = [makeTask("A", "B"), makeTask("B", "A")];
    expect(wouldCreateCycle("dragged", "A", cyclic, 4)).toBe(true);
  });
});

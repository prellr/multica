import { describe, expect, it } from "vitest";
import type { Task } from "@multica/core/types";
import {
  computeInsertPosition,
  computeReparentPosition,
  getSurroundingPositions,
} from "./task-positions";

function makeTask(overrides: Partial<Task> & { id: string; position: number }): Task {
  // Spread overrides FIRST so explicit fields below take precedence —
  // and so the required `id` / `position` from the typed parameter
  // don't appear duplicated to the compiler.
  return {
    workspace_id: "ws-1",
    kind: "task",
    title: "t",
    description: null,
    status: "todo",
    assignee_type: null,
    assignee_id: null,
    creator_type: "member",
    creator_id: "u-1",
    parent_issue_id: null,
    project_id: null,
    start_date: null,
    due_date: null,
    metadata: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("computeInsertPosition", () => {
  it("returns 0 for an empty list (both bounds undefined)", () => {
    expect(computeInsertPosition(undefined, undefined)).toBe(0);
  });

  it("returns after - 1 when inserting at the top", () => {
    expect(computeInsertPosition(undefined, 10)).toBe(9);
  });

  it("returns before + 1 when inserting at the bottom", () => {
    expect(computeInsertPosition(5, undefined)).toBe(6);
  });

  it("returns midpoint between two neighbors", () => {
    expect(computeInsertPosition(0, 10)).toBe(5);
    expect(computeInsertPosition(2, 4)).toBe(3);
  });

  it("handles float positions converging without collisions", () => {
    // After 5 midpoint inserts between 0 and 1 — should stay strictly
    // between, not collide. (Float precision exhausts much later.)
    const lo = 0;
    let hi = 1;
    for (let i = 0; i < 5; i++) {
      const mid = computeInsertPosition(lo, hi);
      expect(mid).toBeGreaterThan(lo);
      expect(mid).toBeLessThan(hi);
      hi = mid;
    }
  });
});

describe("computeReparentPosition", () => {
  it("returns 0 for an empty children set", () => {
    expect(computeReparentPosition([])).toBe(0);
  });

  it("returns max + 1 to append at the end of children", () => {
    const existing = [
      makeTask({ id: "a", position: 0 }),
      makeTask({ id: "b", position: 5 }),
      makeTask({ id: "c", position: 2 }),
    ];
    // Doesn't assume the children are sorted on input — we read max
    // explicitly. Important since the caller filters `existingChildren`
    // and order isn't guaranteed.
    expect(computeReparentPosition(existing)).toBe(6);
  });

  it("handles negative existing positions", () => {
    const existing = [makeTask({ id: "a", position: -3 })];
    expect(computeReparentPosition(existing)).toBe(-2);
  });
});

describe("getSurroundingPositions", () => {
  const list = [
    makeTask({ id: "a", position: 1 }),
    makeTask({ id: "b", position: 2 }),
    makeTask({ id: "c", position: 3 }),
  ];

  it("at index 0 — before is undefined, after is first item", () => {
    expect(getSurroundingPositions(list, 0)).toEqual({ before: undefined, after: 1 });
  });

  it("between two items — before is i-1, after is i", () => {
    expect(getSurroundingPositions(list, 1)).toEqual({ before: 1, after: 2 });
    expect(getSurroundingPositions(list, 2)).toEqual({ before: 2, after: 3 });
  });

  it("at end — before is last, after is undefined", () => {
    expect(getSurroundingPositions(list, list.length)).toEqual({
      before: 3,
      after: undefined,
    });
  });
});

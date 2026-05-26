import type { Task } from "@multica/core/types";

/**
 * Compute the new `position` value when inserting a task between two
 * existing siblings. Uses simple float midpoint — works for at least
 * ~50 reorders before float precision becomes an issue, after which the
 * backend can run a renormalization pass (10, 20, 30...). For v1 we
 * accept that and don't preemptively normalize.
 *
 * @param before Position of the row immediately above the target slot.
 *               `undefined` when dropping at the very top of the list.
 * @param after  Position of the row immediately below the target slot.
 *               `undefined` when dropping at the very bottom of the list.
 */
export function computeInsertPosition(
  before: number | undefined,
  after: number | undefined,
): number {
  if (before === undefined && after === undefined) {
    // Empty list — any position works; start at zero for a tidy default.
    return 0;
  }
  if (before === undefined) {
    // Inserting at the top — go below `after` by a fixed step. Step of 1
    // matches the IncrementIssueCounter cadence in spirit; the exact
    // value doesn't matter as long as it's stable and not equal to
    // `after`.
    return after! - 1;
  }
  if (after === undefined) {
    // Inserting at the bottom.
    return before + 1;
  }
  return (before + after) / 2;
}

/**
 * Resolve the new position when a task is dragged ONTO another task
 * (reparent intent). The dragged task becomes the LAST child of the
 * target — appending at the end matches the user's mental model of
 * "I'm adding this to the bottom of the target's pile" and avoids
 * surprising siblings that already have a hand-curated order.
 *
 * `existingChildren` is the current set of children for the target
 * (excluding the dragged task itself if it was already a child of the
 * target — caller should filter before passing in).
 */
export function computeReparentPosition(existingChildren: Task[]): number {
  if (existingChildren.length === 0) return 0;
  const max = Math.max(...existingChildren.map((t) => t.position));
  return max + 1;
}

/**
 * Find the (before, after) sibling positions for inserting BETWEEN two
 * rows in a list, given the ordered list and the target index. Used by
 * the DnD onDragEnd handler to feed computeInsertPosition.
 *
 * Caller is responsible for excluding the dragged task from `list`
 * before calling — otherwise the math gets confused when the drag is
 * to a position next to the row's existing spot.
 */
export function getSurroundingPositions(
  list: Task[],
  targetIndex: number,
): { before: number | undefined; after: number | undefined } {
  return {
    before: targetIndex > 0 ? list[targetIndex - 1]?.position : undefined,
    after: targetIndex < list.length ? list[targetIndex]?.position : undefined,
  };
}

import type { Task } from "@multica/core/types";

/**
 * Client-side cycle prevention for drag-to-reparent. A task cannot
 * become a child of itself OR of any of its own descendants — that
 * would create a cycle in the parent_issue_id chain.
 *
 * The check walks UP from `candidateParentId` following parent_issue_id
 * pointers; if it reaches `draggedId` along the way, the drop is a
 * cycle. The walk terminates either at a top-level task (parent null)
 * or after `maxDepth` hops (defense against an existing cycle in the
 * data — shouldn't happen, but a runaway loop would freeze the UI).
 *
 * Inputs:
 *  - draggedId: id of the task being dragged.
 *  - candidateParentId: id of the task being dropped onto.
 *  - allTasks: visible task set (main list + children loaded into the
 *    cache). The walk is constrained to this set; if `candidateParent`'s
 *    chain leaves the visible set we conservatively allow the drop —
 *    the server's eventual update is the authoritative check.
 *
 * Returns `true` if the drop would create a cycle (caller should block).
 */
export function wouldCreateCycle(
  draggedId: string,
  candidateParentId: string,
  allTasks: Task[],
  maxDepth = 32,
): boolean {
  if (draggedId === candidateParentId) return true;
  const byId = new Map(allTasks.map((t) => [t.id, t]));
  let current: string | null | undefined = candidateParentId;
  for (let i = 0; i < maxDepth; i++) {
    if (!current) return false;
    if (current === draggedId) return true;
    const node = byId.get(current);
    if (!node) return false; // ran off the visible set — server will validate
    current = node.parent_issue_id;
  }
  // Hit max depth without resolving — assume cycle exists in the data
  // and block the drop. Better to refuse than to deepen the cycle.
  return true;
}

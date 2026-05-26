"use client";

import { useState, useCallback, type ReactNode } from "react";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  KeyboardSensor,
  useSensor,
  useSensors,
  closestCenter,
  type DragEndEvent,
  type DragStartEvent,
  type DragOverEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { toast } from "sonner";
import type { Task } from "@multica/core/types";
import { useUpdateTask } from "@multica/core/tasks";
import { TaskRow } from "./task-row";
import { parseReparentDropId } from "./sortable-task-row";
import {
  computeInsertPosition,
  computeReparentPosition,
  getSurroundingPositions,
} from "../lib/task-positions";
import { wouldCreateCycle } from "../lib/task-tree";
import { useT } from "../../i18n";

export interface TaskDndContextProps {
  /** Ordered list of tasks the DnD context governs. For the main list:
   *  the visible (filtered, scoped) task array. For the children list:
   *  the children of the open task. */
  items: Task[];
  /** Visible cross-list set used for cycle detection — typically the
   *  union of `items` plus any other tasks loaded in the cache (e.g.
   *  when reparenting in the main list, pass main-list tasks; the
   *  children of the dragged task should be in the cache via the
   *  detail's children query so the cycle walk can see them). Passing
   *  just `items` is safe; missing nodes fall back to "server will
   *  validate." */
  cycleScope?: Task[];
  /** When true, dropping a task onto another row in this list
   *  reparents it. Off for the children-list (subtasks reorder only;
   *  reparenting onto a sibling would create a grandchild). */
  acceptReparent?: boolean;
  /** When this list represents the children of a parent, pass the
   *  parent id so an item dragged within this context retains the same
   *  parent (a reorder shouldn't accidentally promote a subtask to
   *  top-level). When omitted, items in this context are top-level
   *  (parent_issue_id = null) and stay top-level on reorder. */
  parentScope?: string | null;
  children: ReactNode;
}

/**
 * Shared DnD wiring for a task list. Renders a DndContext +
 * SortableContext around `children`, computes the position/parent
 * patch on drop, and dispatches it via useUpdateTask (optimistic).
 *
 * Layered semantics:
 *  - Drop BETWEEN rows → reorder. Compute midpoint of new neighbors.
 *  - Drop ON a row (inside its reparent zone) → reparent. Compute
 *    position at end of target's children.
 *  - Cycle prevention: a task can't be reparented onto itself or any
 *    of its descendants. Checked client-side via wouldCreateCycle;
 *    a violating drop is rolled back with a toast.
 *
 * The reparent zone is opt-in per row (acceptReparent prop on
 * SortableTaskRow + this context). Children lists pass false so
 * subtasks can be reordered but not re-nested onto siblings.
 */
export function TaskDndProvider({
  items,
  cycleScope,
  acceptReparent = false,
  parentScope = null,
  children,
}: TaskDndContextProps) {
  const { t } = useT("tasks");
  const updateTask = useUpdateTask();
  const [activeId, setActiveId] = useState<string | null>(null);
  const [reparentTargetId, setReparentTargetId] = useState<string | null>(null);

  const sensors = useSensors(
    // Activation distance prevents accidental drags when the user
    // intends to click the row (status toggle / link). 6px matches the
    // issues board which has the same dual click/drag interaction.
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const sortableIds = items.map((t) => t.id);
  const activeTask = activeId ? items.find((t) => t.id === activeId) ?? null : null;

  const onDragStart = (event: DragStartEvent) => {
    setActiveId(String(event.active.id));
  };

  const onDragOver = useCallback((event: DragOverEvent) => {
    // Track whether the cursor is currently over a reparent zone so we
    // can render the target highlight. We render the highlight via
    // isReparentTarget prop on SortableTaskRow.
    const overId = event.over ? String(event.over.id) : null;
    const reparentId = overId ? parseReparentDropId(overId) : null;
    setReparentTargetId(reparentId);
  }, []);

  const onDragEnd = (event: DragEndEvent) => {
    const draggedId = String(event.active.id);
    const overId = event.over ? String(event.over.id) : null;
    setActiveId(null);
    setReparentTargetId(null);
    if (!overId) return;

    // ----- Reparent intent -----
    const reparentTargetId = parseReparentDropId(overId);
    if (reparentTargetId) {
      if (reparentTargetId === draggedId) return;
      const scope = cycleScope ?? items;
      if (wouldCreateCycle(draggedId, reparentTargetId, scope)) {
        toast.error(t(($) => $.dnd.cycle_blocked));
        return;
      }
      // New parent's existing children — exclude the dragged task in
      // case it's already a child of the same parent (no-op reparent
      // shouldn't double-count).
      const newSiblings = scope.filter(
        (s) => s.parent_issue_id === reparentTargetId && s.id !== draggedId,
      );
      const position = computeReparentPosition(newSiblings);
      updateTask.mutate(
        { id: draggedId, patch: { parent_issue_id: reparentTargetId, position } },
        { onError: () => toast.error(t(($) => $.dnd.reorder_failed)) },
      );
      return;
    }

    // ----- Reorder intent -----
    // dnd-kit's sortable computes `over.id` as the row the dragged
    // item would land "near." Build the new order by removing the
    // dragged item and inserting it at the over row's index.
    if (draggedId === overId) return;
    const fromIndex = items.findIndex((t) => t.id === draggedId);
    const toIndex = items.findIndex((t) => t.id === overId);
    if (fromIndex < 0 || toIndex < 0) return;

    // Reconstruct the list as it would be after the move so we can read
    // the new neighbors' positions for the midpoint math.
    const without = items.filter((_, i) => i !== fromIndex);
    const insertAt = toIndex > fromIndex ? toIndex : toIndex;
    const surrounding = getSurroundingPositions(without, insertAt);
    const position = computeInsertPosition(surrounding.before, surrounding.after);

    // For a reorder within this context, preserve the parent scope —
    // a drag within the main list keeps parent_issue_id = null (top
    // level); a drag within a children list keeps the parent. Without
    // passing parent_issue_id explicitly, UpdateTask's sqlc.narg would
    // CLEAR the value (sets to NULL), which would promote a subtask to
    // top-level on every reorder. That's the bug this guard prevents.
    const patch: { position: number; parent_issue_id?: string | null } = { position };
    if (parentScope !== null) {
      patch.parent_issue_id = parentScope;
    }
    updateTask.mutate(
      { id: draggedId, patch },
      { onError: () => toast.error(t(($) => $.dnd.reorder_failed)) },
    );
  };

  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragEnd={onDragEnd}
    >
      <SortableContext items={sortableIds} strategy={verticalListSortingStrategy}>
        <ReparentTargetContext.Provider value={reparentTargetId}>
          <AcceptReparentContext.Provider value={acceptReparent}>
            {children}
          </AcceptReparentContext.Provider>
        </ReparentTargetContext.Provider>
      </SortableContext>
      <DragOverlay>
        {activeTask ? (
          // Render the dragged row as a non-interactive clone in the
          // overlay layer so it follows the cursor cleanly across panels
          // (sortable's in-place transform doesn't escape its container).
          <div className="bg-background shadow-lg rounded border">
            <TaskRow task={activeTask} />
          </div>
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}

// Internal contexts so SortableTaskRow can read drag state without
// prop-drilling through whatever the list renders between the provider
// and the row. Kept local to this file to avoid expanding the public
// API surface — these are an implementation detail of the DnD wiring.
import { createContext, useContext } from "react";
const ReparentTargetContext = createContext<string | null>(null);
const AcceptReparentContext = createContext<boolean>(false);
export function useReparentTargetId(): string | null {
  return useContext(ReparentTargetContext);
}
export function useAcceptReparent(): boolean {
  return useContext(AcceptReparentContext);
}

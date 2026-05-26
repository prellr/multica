"use client";

import { useDroppable } from "@dnd-kit/core";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import type { Task } from "@multica/core/types";
import { TaskRow } from "./task-row";
import { useReparentTargetId, useAcceptReparent } from "./task-dnd-context";

interface SortableTaskRowProps {
  task: Task;
  selected?: boolean;
}

/**
 * TaskRow wrapped for drag-and-drop. Adds two pieces of wiring on top of
 * the bare row:
 *   1. useSortable — participates in vertical reorder within the
 *      SortableContext that wraps the parent list. The whole row is
 *      sortable; cursor anywhere on it starts a drag.
 *   2. useDroppable on an inner zone covering the row middle —
 *      identifies this row as a "reparent target" when the cursor lands
 *      in the center, distinct from the reorder-above/reorder-below
 *      sortable slots at the edges. dnd-kit's closestCenter collision
 *      detection picks the closer of the two automatically.
 *
 * The drag handle (grip icon) only shows on row hover so the row stays
 * visually clean at rest. The whole row remains the drag activator —
 * the grip is a discoverability affordance, not the only target.
 */
export function SortableTaskRow({ task, selected = false }: SortableTaskRowProps) {
  const sortable = useSortable({ id: task.id });
  const reparentTargetId = useReparentTargetId();
  const acceptReparent = useAcceptReparent();
  const isReparentTarget = reparentTargetId === task.id;
  const reparent = useDroppable({
    id: reparentDropId(task.id),
    // Disable when reparent isn't accepted (children-list mounts), and
    // when the row is being dragged itself — can't reparent onto self.
    disabled: !acceptReparent || sortable.isDragging,
    data: { kind: "reparent", taskId: task.id },
  });

  const style: React.CSSProperties = {
    transform: CSS.Transform.toString(sortable.transform),
    transition: sortable.transition,
  };

  return (
    <div
      ref={sortable.setNodeRef}
      style={style}
      className={cn(
        "group/dnd relative",
        // While dragging, dim the source row so the drag overlay reads
        // as "the thing moving" — the original spot is the empty
        // placeholder dnd-kit's sortable rendering already animates.
        sortable.isDragging && "opacity-30",
      )}
    >
      {/* Reparent drop zone — covers the middle ~60% of the row. The
        * top/bottom 20% gaps are where the sortable reorder slots live.
        * `pointer-events-none` on the inner content lets clicks pass
        * through to the row's link; the droppable wiring only needs the
        * ref to compute hit-testing, not actual pointer interaction. */}
      {acceptReparent && (
        <div
          ref={reparent.setNodeRef}
          aria-hidden
          className="pointer-events-none absolute inset-x-0 top-[20%] bottom-[20%]"
        />
      )}

      {/* Reparent-over highlight — drawn outside the TaskRow so it
        * doesn't fight the row's own selected/hover backgrounds.
        * Drawn with inset 1px so it reads as "this row will adopt"
        * rather than "this row is being clicked." */}
      {isReparentTarget && (
        <div
          aria-hidden
          className="pointer-events-none absolute inset-x-1 inset-y-0.5 rounded ring-2 ring-primary/60 bg-primary/5"
        />
      )}

      {/* Grip handle. Drag activator is the whole row (the sortable
        * listeners apply to setNodeRef'd container above via attributes
        * + listeners spread). The grip is purely a discoverability hint;
        * the click target is the link in TaskRow. */}
      <span
        {...sortable.attributes}
        {...sortable.listeners}
        // Stop pointer events from bubbling to AppLink so a drag-start
        // doesn't immediately follow the link. The link click is
        // separate — onClick from a real click bubbles normally.
        onPointerDown={(e) => e.stopPropagation()}
        aria-label={`Drag ${task.title}`}
        className="absolute left-0 top-1/2 -translate-y-1/2 -translate-x-3 flex h-5 w-3 cursor-grab items-center justify-center text-muted-foreground/40 opacity-0 transition-opacity group-hover/dnd:opacity-100 active:cursor-grabbing"
      >
        <GripVertical className="h-3 w-3" aria-hidden />
      </span>

      <TaskRow task={task} selected={selected} />
    </div>
  );
}

/** Stable id for the reparent droppable so the onDragEnd handler can
 *  distinguish a reparent drop from a reorder drop by inspecting
 *  `over.id`. Kept as a function rather than a template literal at
 *  callsite so the prefix stays in one place. */
export function reparentDropId(taskId: string): string {
  return `${taskId}:reparent`;
}

/** Inverse of {@link reparentDropId}. Returns the underlying task id
 *  when the droppable id is a reparent target, otherwise null. */
export function parseReparentDropId(dropId: string): string | null {
  const suffix = ":reparent";
  return dropId.endsWith(suffix) ? dropId.slice(0, -suffix.length) : null;
}

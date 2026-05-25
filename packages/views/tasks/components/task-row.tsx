"use client";

import { CircleCheck, Circle, CircleDashed, CircleX } from "lucide-react";
import type { Task, TaskStatus } from "@multica/core/types";
import { useCurrentWorkspace } from "@multica/core/paths";
import { paths } from "@multica/core/paths";
import { useUpdateTask, isTempTaskId } from "@multica/core/tasks";
import { cn } from "@multica/ui/lib/utils";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

// Status → icon mapping. Mirrors the iconography of the issue surface so
// the visual language stays consistent. Each icon is small (16px) so a row
// stays roughly the height of a single line of body text.
const STATUS_ICON: Record<TaskStatus, React.ComponentType<{ className?: string }>> = {
  todo: Circle,
  in_progress: CircleDashed,
  done: CircleCheck,
  cancelled: CircleX,
};

const STATUS_COLOR: Record<TaskStatus, string> = {
  todo: "text-muted-foreground",
  in_progress: "text-blue-500",
  done: "text-green-500",
  cancelled: "text-muted-foreground/60",
};

interface TaskRowProps {
  task: Task;
  /** Master-detail highlight. The page passes `task.id === ?task` so the
   *  currently-open row visually stays anchored when the sidebar is
   *  open. Omit when the row is rendered outside a master-detail (e.g.
   *  embedded in a children list inside the sidebar itself — though in
   *  that case clicking still re-routes selection to the child). */
  selected?: boolean;
}

/**
 * Cycle status when the user clicks the status icon. `todo` and `in_progress`
 * complete to `done`; `done` and `cancelled` re-open to `todo`. Mirrors a
 * traditional checkbox while still letting the underlying status model carry
 * the richer in_progress / cancelled states the UI doesn't surface directly
 * (those are settable from the detail page, future PR).
 */
function nextStatusForToggle(s: TaskStatus): TaskStatus {
  if (s === "done" || s === "cancelled") return "todo";
  return "done";
}

export function TaskRow({ task, selected = false }: TaskRowProps) {
  const { t } = useT("tasks");
  const workspace = useCurrentWorkspace();
  const updateTask = useUpdateTask();
  if (!workspace) return null;
  const href = paths.workspace(workspace.slug).taskDetail(task.id);

  const Icon = STATUS_ICON[task.status] ?? Circle;
  const statusLabel = t(($) => $.status[task.status]) ?? task.status;
  // Optimistic rows (created in-flight) can't be mutated until the server
  // assigns the real id — clicking the toggle would route the patch to a
  // temp id the server doesn't know. Disable the toggle for the brief
  // window between optimistic-insert and onSuccess.
  const isPending = isTempTaskId(task.id);

  const onToggleStatus = (e: React.MouseEvent) => {
    // Stop propagation so the surrounding link doesn't navigate away;
    // preventDefault also stops the click from triggering a parent <a>.
    e.preventDefault();
    e.stopPropagation();
    if (isPending) return;
    updateTask.mutate({ id: task.id, patch: { status: nextStatusForToggle(task.status) } });
  };

  return (
    <AppLink
      href={href}
      aria-current={selected ? "true" : undefined}
      className={cn(
        "group flex items-center gap-3 border-b px-4 py-2.5 hover:bg-muted/40 focus:bg-muted/60 focus:outline-none",
        // Selection highlight: muted background plus a 2px left accent
        // so the row stays visually anchored when the sidebar opens.
        selected && "bg-muted/60 hover:bg-muted/60",
      )}
    >
      <button
        type="button"
        onClick={onToggleStatus}
        disabled={isPending}
        aria-label={statusLabel}
        // h-5 w-5 with the icon centered gives a slightly larger hit target
        // than the visual icon — important on touch + makes the click feel
        // intentional. Hover ring is subtle; the focus ring follows
        // shadcn's default for keyboard users.
        className="-m-1 flex h-5 w-5 shrink-0 items-center justify-center rounded-full p-0.5 hover:bg-muted focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-ring disabled:opacity-50"
      >
        <Icon className={cn("h-4 w-4", STATUS_COLOR[task.status])} aria-hidden />
      </button>
      <span
        className={cn(
          "flex-1 truncate text-sm",
          // Done/cancelled get a struck-through, muted treatment so a list
          // mixed across statuses visually emphasizes what's still open.
          (task.status === "done" || task.status === "cancelled") &&
            "text-muted-foreground line-through decoration-1",
        )}
      >
        {task.title}
      </span>
    </AppLink>
  );
}

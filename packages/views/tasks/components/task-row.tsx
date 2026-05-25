"use client";

import { CircleCheck, Circle, CircleDashed, CircleX } from "lucide-react";
import type { Task, TaskStatus } from "@multica/core/types";
import { useCurrentWorkspace } from "@multica/core/paths";
import { paths } from "@multica/core/paths";
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
}

export function TaskRow({ task }: TaskRowProps) {
  const { t } = useT("tasks");
  const workspace = useCurrentWorkspace();
  if (!workspace) return null;
  const href = paths.workspace(workspace.slug).taskDetail(task.id);

  const Icon = STATUS_ICON[task.status] ?? Circle;
  const statusLabel = t(($) => $.status[task.status]) ?? task.status;

  return (
    <AppLink
      href={href}
      className="group flex items-center gap-3 border-b px-4 py-2.5 hover:bg-muted/40 focus:bg-muted/60 focus:outline-none"
    >
      <Icon
        className={cn("h-4 w-4 shrink-0", STATUS_COLOR[task.status])}
        aria-label={statusLabel}
      />
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

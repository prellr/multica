"use client";

import { useQuery } from "@tanstack/react-query";
import { taskListOptions } from "@multica/core/tasks";
import { useWorkspaceId } from "@multica/core/hooks";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { TaskRow } from "./task-row";
import { QuickAddTask } from "./quick-add-task";
import { useT } from "../../i18n";

interface TaskChildrenListProps {
  parentId: string;
}

/**
 * Renders the children of a task as a small list with an inline "Add
 * subtask" input. Lives inside the task detail sidebar.
 *
 * Cache reuse: the existing taskListOptions query takes parent_issue_id
 * as a filter and bakes it into the queryKey, so the children query gets
 * its own cache entry that doesn't collide with the top-level list. WS
 * invalidation on task:created/updated/deleted patches both caches via
 * the prefix match in the cache helpers — no extra wiring needed here.
 *
 * The inline QuickAddTask is bound to `parentId` so every task created
 * from this surface gets `parent_issue_id = parentId`. The compact size
 * variant trims the visual weight so the input feels like a continuation
 * of the children list rather than competing with the parent's title
 * area above.
 */
export function TaskChildrenList({ parentId }: TaskChildrenListProps) {
  const { t } = useT("tasks");
  const wsId = useWorkspaceId();
  const { data: children = [], isLoading } = useQuery(
    taskListOptions(wsId, { parent_issue_id: parentId }),
  );

  return (
    <section className="mt-8">
      <h2 className="px-4 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {t(($) => $.detail.subtasks_heading)}
      </h2>
      <div className="mt-2 border-y bg-background">
        <QuickAddTask
          parentIssueId={parentId}
          placeholder={t(($) => $.quick_add.subtask_placeholder)}
          size="compact"
        />
        {isLoading ? (
          <div className="flex items-center gap-3 px-3 py-1.5">
            <Skeleton className="h-3.5 w-3.5 rounded-full" />
            <Skeleton className="h-3 flex-1 max-w-sm" />
          </div>
        ) : children.length === 0 ? (
          <div className="px-4 py-3 text-xs text-muted-foreground">
            {t(($) => $.detail.subtasks_empty)}
          </div>
        ) : (
          children.map((child) => <TaskRow key={child.id} task={child} />)
        )}
      </div>
    </section>
  );
}

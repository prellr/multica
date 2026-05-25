"use client";

import { ListTodo } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { taskListOptions } from "@multica/core/tasks";
import { useWorkspaceId } from "@multica/core/hooks";
import { PageHeader } from "../../layout/page-header";
import { useT } from "../../i18n";
import { TaskRow } from "./task-row";

/**
 * Workspace task list. Flat by design — no per-status buckets, no kanban,
 * no view-mode switcher. Tasks are intended to be lighter than issues; if
 * the surface grows the equivalent of {@link IssuesPage}, the lightness
 * thesis has failed and we should consolidate the two surfaces instead.
 *
 * PR 3a is read-only. PR 4 adds inline quick-add (the differentiating UX);
 * PR 5+ may add filtering, grouping, or "my tasks" tabs depending on what
 * users actually do.
 */
export function TasksPage() {
  const { t } = useT("tasks");
  const wsId = useWorkspaceId();
  const { data: tasks = [], isLoading } = useQuery(taskListOptions(wsId));

  return (
    <div className="flex h-full flex-col">
      <PageHeader>
        <ListTodo className="mr-2 h-4 w-4 text-muted-foreground" aria-hidden />
        <span className="text-sm font-medium">{t(($) => $.page.title)}</span>
      </PageHeader>

      {isLoading ? (
        <div className="flex-1 overflow-auto">
          {Array.from({ length: 8 }).map((_, i) => (
            // Match TaskRow's visual height so the layout doesn't jump on
            // load — same py-2.5 + h-4 icon as the real row.
            <div key={i} className="flex items-center gap-3 border-b px-4 py-2.5">
              <Skeleton className="h-4 w-4 rounded-full" />
              <Skeleton className="h-4 flex-1 max-w-md" />
            </div>
          ))}
        </div>
      ) : tasks.length === 0 ? (
        <EmptyState />
      ) : (
        <div className="flex-1 overflow-auto">
          {tasks.map((task) => (
            <TaskRow key={task.id} task={task} />
          ))}
        </div>
      )}
    </div>
  );
}

function EmptyState() {
  const { t } = useT("tasks");
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
      <ListTodo className="h-10 w-10 text-muted-foreground" aria-hidden />
      <div>
        <h2 className="text-lg font-medium">{t(($) => $.page.empty_title)}</h2>
        <p className="mt-1 max-w-md text-sm text-muted-foreground">
          {t(($) => $.page.empty_description)}
        </p>
      </div>
    </div>
  );
}

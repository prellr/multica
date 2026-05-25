"use client";

import { ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { taskDetailOptions } from "@multica/core/tasks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { paths } from "@multica/core/paths";
import { PageHeader } from "../../layout/page-header";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

interface TaskDetailPageProps {
  taskId: string;
}

/**
 * Read-only task detail view. PR 3a deliberately ships without inline
 * editing — mutations land in PR 4 alongside the quick-add UX so the
 * create/edit flows can share components.
 */
export function TaskDetailPage({ taskId }: TaskDetailPageProps) {
  const { t } = useT("tasks");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const { data: task, isLoading, isError } = useQuery(taskDetailOptions(wsId, taskId));

  const backHref = workspace ? paths.workspace(workspace.slug).tasks() : "#";

  return (
    <div className="flex h-full flex-col">
      <PageHeader>
        <AppLink
          href={backHref}
          className="mr-2 inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-4 w-4" aria-hidden />
          <span>{t(($) => $.detail.back_to_tasks)}</span>
        </AppLink>
      </PageHeader>

      <div className="flex-1 overflow-auto px-6 py-4">
        {isLoading ? (
          <div className="max-w-3xl space-y-4">
            <Skeleton className="h-7 w-2/3" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-5/6" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        ) : isError || !task ? (
          <p className="text-sm text-muted-foreground">{t(($) => $.errors.load_failed)}</p>
        ) : (
          <article className="max-w-3xl">
            <h1 className="text-xl font-semibold">{task.title}</h1>
            <div className="mt-1 text-xs text-muted-foreground">
              {t(($) => $.status[task.status])}
            </div>
            <div className="mt-6 whitespace-pre-wrap text-sm leading-relaxed">
              {task.description ?? (
                <span className="text-muted-foreground">{t(($) => $.detail.no_description)}</span>
              )}
            </div>
          </article>
        )}
      </div>
    </div>
  );
}

"use client";

import { useState } from "react";
import { ArrowLeft, ArrowUpRight, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { taskDetailOptions, useDeleteTask, usePromoteTask } from "@multica/core/tasks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import { paths } from "@multica/core/paths";
import { PageHeader } from "../../layout/page-header";
import { AppLink } from "../../navigation";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";

interface TaskDetailPageProps {
  taskId: string;
}

/**
 * Read-only task detail view + Promote-to-issue affordance. Inline editing
 * still lives on the to-do list (clicking the status icon on a row) and
 * the quick-add input — this page is for the "I want to see context"
 * moment, plus the one-shot promote action.
 *
 * After a successful promote, navigate to the issue's URL. The task UUID
 * is preserved by the server (the row's just flipped + numbered), so
 * /tasks/:id and /issues/:id resolve to the same physical row — but
 * /tasks/:id now 404s (the GetTask query filters on kind='task'), so
 * staying on this route would render the loader's error state.
 */
export function TaskDetailPage({ taskId }: TaskDetailPageProps) {
  const { t } = useT("tasks");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const navigation = useNavigation();
  const { data: task, isLoading, isError } = useQuery(taskDetailOptions(wsId, taskId));
  const promote = usePromoteTask();
  const deleteTask = useDeleteTask();
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  const backHref = workspace ? paths.workspace(workspace.slug).tasks() : "#";

  const onPromote = () => {
    if (!task || promote.isPending) return;
    promote.mutate(task.id, {
      onSuccess: (issue) => {
        toast.success(t(($) => $.detail.promote_success, { identifier: issue.identifier }));
        if (workspace) {
          navigation.push(paths.workspace(workspace.slug).issueDetail(issue.id));
        }
      },
      onError: () => {
        toast.error(t(($) => $.detail.promote_failed));
      },
    });
  };

  const onDeleteConfirmed = () => {
    if (!task || deleteTask.isPending) return;
    deleteTask.mutate(task.id, {
      onSuccess: () => {
        setConfirmingDelete(false);
        // Navigate back to the list before the user notices the detail
        // route now 404s. Toast omitted — the row disappearing from the
        // list is its own confirmation; piling a toast on a confirm-then-
        // act flow is noise.
        if (workspace) {
          navigation.push(paths.workspace(workspace.slug).tasks());
        }
      },
      onError: () => {
        toast.error(t(($) => $.detail.delete_failed));
      },
    });
  };

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
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0 flex-1">
                <h1 className="text-xl font-semibold">{task.title}</h1>
                <div className="mt-1 text-xs text-muted-foreground">
                  {t(($) => $.status[task.status])}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={onPromote}
                  disabled={promote.isPending || deleteTask.isPending}
                >
                  <ArrowUpRight className="mr-1.5 h-3.5 w-3.5" aria-hidden />
                  {promote.isPending
                    ? t(($) => $.detail.promote_pending)
                    : t(($) => $.detail.promote_button)}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setConfirmingDelete(true)}
                  disabled={promote.isPending || deleteTask.isPending}
                  aria-label={t(($) => $.detail.delete_button)}
                  // Icon-only button so the destructive action doesn't visually
                  // compete with Promote (the constructive primary). Hover
                  // surfaces destructive-red to make the intent obvious.
                  className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                >
                  <Trash2 className="h-4 w-4" aria-hidden />
                </Button>
              </div>
            </div>
            <div className="mt-6 whitespace-pre-wrap text-sm leading-relaxed">
              {task.description ?? (
                <span className="text-muted-foreground">{t(($) => $.detail.no_description)}</span>
              )}
            </div>
          </article>
        )}
      </div>

      <AlertDialog open={confirmingDelete} onOpenChange={setConfirmingDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.detail.delete_confirm_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.detail.delete_confirm_body)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteTask.isPending}>
              {t(($) => $.detail.delete_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction
              onClick={onDeleteConfirmed}
              disabled={deleteTask.isPending}
              variant="destructive"
            >
              {t(($) => $.detail.delete_confirm_action)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

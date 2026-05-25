"use client";

import { useState } from "react";
import { ArrowUpRight, Trash2, X } from "lucide-react";
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
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { TaskChildrenList } from "./task-children-list";

interface TaskDetailSidebarProps {
  taskId: string;
  /** Clears the selection in the parent page; URL syncs accordingly. */
  onClose: () => void;
}

/**
 * Task detail rendered as the right panel of the master-detail layout on
 * the tasks page. Replaces the old standalone `/tasks/:id` page surface —
 * staying on `/tasks` keeps the list visible for context-switching
 * between tasks and matches the inbox UX.
 *
 * Layout: close button + header row (title, status, promote, delete) +
 * description + subtasks section. The subtasks section is the parent/
 * child surface: rendered as a small inline list with a compact
 * "Add subtask" input that binds the new task's parent to this task's id.
 */
export function TaskDetailSidebar({ taskId, onClose }: TaskDetailSidebarProps) {
  const { t } = useT("tasks");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const navigation = useNavigation();
  const { data: task, isLoading, isError } = useQuery(taskDetailOptions(wsId, taskId));
  const promote = usePromoteTask();
  const deleteTask = useDeleteTask();
  const [confirmingDelete, setConfirmingDelete] = useState(false);

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
        // Clear selection — the row's gone, the panel shouldn't keep
        // showing it. URL syncs via the parent page's onClose handler.
        // No toast: the row disappearing from the list is its own
        // confirmation, and a toast on top of a confirm-then-act flow
        // is noise.
        onClose();
      },
      onError: () => {
        toast.error(t(($) => $.detail.delete_failed));
      },
    });
  };

  return (
    <div className="flex h-full flex-col">
      {/* Compact header with just a close button. Mirrors the height of
        * the list's PageHeader on the left so the visual top edges line
        * up across the master-detail seam. */}
      <div className="flex h-12 shrink-0 items-center justify-end border-b px-3">
        <Button
          size="icon"
          variant="ghost"
          onClick={onClose}
          aria-label={t(($) => $.detail.close)}
          className="h-7 w-7 text-muted-foreground hover:text-foreground"
        >
          <X className="h-4 w-4" aria-hidden />
        </Button>
      </div>

      <div className="flex-1 overflow-auto py-4">
        {isLoading ? (
          <div className="space-y-4 px-6">
            <Skeleton className="h-7 w-2/3" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-5/6" />
          </div>
        ) : isError || !task ? (
          <p className="px-6 text-sm text-muted-foreground">{t(($) => $.errors.load_failed)}</p>
        ) : (
          <>
            <article className="px-6">
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
                    className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                  >
                    <Trash2 className="h-4 w-4" aria-hidden />
                  </Button>
                </div>
              </div>
              <div className="mt-6 whitespace-pre-wrap text-sm leading-relaxed">
                {task.description ?? (
                  <span className="text-muted-foreground">
                    {t(($) => $.detail.no_description)}
                  </span>
                )}
              </div>
            </article>

            <TaskChildrenList parentId={task.id} />
          </>
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

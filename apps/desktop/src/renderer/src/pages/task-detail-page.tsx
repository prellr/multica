import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { TaskDetailPage as ViewTaskDetailPage } from "@multica/views/tasks";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";
import { useWorkspaceId } from "@multica/core/hooks";
import { taskDetailOptions } from "@multica/core/tasks";
import { useDocumentTitle } from "@/hooks/use-document-title";

/**
 * Desktop wrapper for the shared TaskDetailPage. Mirrors issue-detail-page
 * but uses the task's title for the document title — tasks have no
 * human-readable identifier (no `MUL-123` prefix), so we lean on the title
 * alone. Tab/window titles flow from useDocumentTitle.
 */
export function TaskDetailPage() {
  const { id } = useParams<{ id: string }>();
  const wsId = useWorkspaceId();
  const { data: task } = useQuery(taskDetailOptions(wsId, id!));

  useDocumentTitle(task?.title ?? "Task");

  if (!id) return null;
  return (
    <ErrorBoundary resetKeys={[id]}>
      <ViewTaskDetailPage taskId={id} />
    </ErrorBoundary>
  );
}

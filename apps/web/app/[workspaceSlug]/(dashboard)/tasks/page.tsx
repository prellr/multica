"use client";

import { TasksPage } from "@multica/views/tasks";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function Page() {
  return (
    <ErrorBoundary>
      <TasksPage />
    </ErrorBoundary>
  );
}

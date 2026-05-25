"use client";

import { use } from "react";
import { TaskDetailPage } from "@multica/views/tasks";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function Page({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return (
    <ErrorBoundary resetKeys={[id]}>
      <TaskDetailPage taskId={id} />
    </ErrorBoundary>
  );
}

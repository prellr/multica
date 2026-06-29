"use client";

import { DraftsPage } from "@multica/views/drafts";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function Page() {
  return (
    <ErrorBoundary>
      <DraftsPage />
    </ErrorBoundary>
  );
}

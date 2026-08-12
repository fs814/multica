"use client";

import { WorkflowIssuesPage } from "@multica/views/issues/components";
import { ErrorBoundary } from "@multica/ui/components/common/error-boundary";

export default function Page() {
  return (
    <ErrorBoundary>
      <WorkflowIssuesPage />
    </ErrorBoundary>
  );
}

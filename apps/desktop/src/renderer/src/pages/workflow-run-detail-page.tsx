import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { WorkflowRunDetailPage as WorkflowRunDetail } from "@multica/views/workflows/components";
import { useWorkspaceId } from "@multica/core/hooks";
import { workflowRunDetailOptions } from "@multica/core/workflows/queries";
import { useDocumentTitle } from "@/hooks/use-document-title";

/**
 * Desktop wrapper for the shared workflow-run detail view.
 *
 * The shared view takes the id as a prop so it stays router-agnostic (web passes
 * the Next.js route param); this wrapper reads react-router's param and — the
 * reason the file exists — feeds a title into the tab.
 *
 * The title is gated on `status`, not on the object being present:
 * `getWorkflowRun` spreads the requested id onto its parse-miss fallback, so a
 * truthy `data` says nothing about whether the response was readable, and an
 * unreadable one would title the tab with an empty template name. See
 * EMPTY_WORKFLOW_RUN_DETAIL. A run has no name of its own, so the input's title
 * is preferred over the template's — otherwise every Bug Fix run tab reads
 * "Bug Fix".
 */
export function WorkflowRunDetailPage() {
  const { id } = useParams<{ id: string }>();
  const wsId = useWorkspaceId();
  const { data } = useQuery(workflowRunDetailOptions(wsId, id ?? ""));

  const inputTitle =
    data?.status && typeof data.input.title === "string"
      ? data.input.title.trim()
      : "";
  const templateName = data?.status ? data.template_name : "";
  useDocumentTitle(inputTitle || templateName || "Run");

  if (!id) return null;
  return <WorkflowRunDetail runId={id} />;
}

import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { WorkflowTemplateDetailPage as WorkflowTemplateDetail } from "@multica/views/workflows/components";
import { useWorkspaceId } from "@multica/core/hooks";
import { workflowTemplateDetailOptions } from "@multica/core/workflows/queries";
import { useDocumentTitle } from "@/hooks/use-document-title";

/**
 * Desktop wrapper for the shared workflow-template detail view.
 *
 * The shared view takes the id as a prop so it stays router-agnostic (web
 * passes the Next.js route param); the desktop wrapper is what reads
 * react-router's param and — the reason this file exists at all — feeds the
 * template name into the tab title. Plain text only, no status glyph
 * prefix (MUL-4370).
 */
export function WorkflowTemplateDetailPage() {
  const { id } = useParams<{ id: string }>();
  const wsId = useWorkspaceId();
  const { data } = useQuery(workflowTemplateDetailOptions(wsId, id ?? ""));

  // parseWithFallback can hand back the EMPTY_ detail on a malformed
  // response, so an empty name falls through to the static label rather
  // than blanking the tab.
  useDocumentTitle(data?.name || "Workflow");

  if (!id) return null;
  return <WorkflowTemplateDetail templateId={id} />;
}

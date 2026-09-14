import { useParams } from "react-router-dom";
import { WorkflowDebugDetailPage } from "@multica/views/workflows/components";
import { useDocumentTitle } from "@/hooks/use-document-title";
import { useT } from "@multica/views/i18n";
export function WorkflowDebugPage() {
 const {id} = useParams<{id:string}>(); const {t}=useT("workflows"); useDocumentTitle(t($=>$.debug.title));
 return id ? <WorkflowDebugDetailPage key={id} runId={id}/> : null;
}

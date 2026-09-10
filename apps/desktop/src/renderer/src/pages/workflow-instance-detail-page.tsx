import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { WorkflowInstanceDetailPage as Detail } from "@multica/views/workflows/components";
import { useWorkspaceId } from "@multica/core/hooks";
import { workflowInstanceOptions } from "@multica/core/workflows";
import { useDocumentTitle } from "@/hooks/use-document-title";
export function WorkflowInstanceDetailPage(){
 const {id}=useParams<{id:string}>();const ws=useWorkspaceId();
 const {data}=useQuery(workflowInstanceOptions(ws,id??""));
 useDocumentTitle(data?.name||"Workflow instance");
 return id?<Detail instanceId={id}/>:null;
}

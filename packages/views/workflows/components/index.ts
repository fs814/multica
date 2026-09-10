export { WorkflowsPage } from "./workflows-page";
export { WorkflowTemplateDetailPage } from "./workflow-template-detail-page";
// The runs surface ships through the same entry point as the templates it
// executes: apps only declare `@multica/views/workflows/components` once, and a
// second export path would let a route import a run page without the template
// pages' i18n namespace being registered.
export {
  WorkflowRunsPage,
  WorkflowRunDetailPage,
  WorkflowRunDialog,
} from "../runs/components";

export { WorkflowInstancesPage } from "../instances/workflow-instances-page";
export { WorkflowInstanceDetailPage } from "../instances/workflow-instance-detail-page";

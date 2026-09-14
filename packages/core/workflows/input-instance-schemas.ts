import { z } from "zod";
import {
  WorkflowRunSchema,
  WorkflowNodeSchema,
  WorkflowDefinitionSchema,
  type WorkflowNode,
} from "./schemas";

export const WorkflowInputInstanceSchema = z
  .object({
    template_name: z.string().optional().default(""),
    version_number: z.number().optional().default(0),
    editor_name: z.string().optional().default(""),
    latest_run_id: z.string().optional().default(""),
    latest_run_status: z.string().optional().default(""),
    description: z.string().optional().default(""),
    input_node: WorkflowNodeSchema.nullable().optional().default(null),
    image_attachment_id: z.string().nullable().optional().default(null),
    archived_at: z.string().nullable().optional().default(null),
    updated_by_id: z.string().nullable().optional().default(null),
    id: z.string().min(1),
    template_id: z.string().min(1),
    template_version_id: z.string().nullable().optional().default(null),
    created_by_id: z.string().nullable().optional().default(null),
    created_at: z.string().optional().default(""),
    name: z.string().min(1),
    input: z.record(z.string(), z.string()),
    project_id: z.string().nullable().optional().default(null),
    revision: z.number().int().positive(),
    updated_at: z.string().optional().default(""),
  })
  .transform((row) => ({
    templateName: row.template_name,
    versionNumber: row.version_number,
    editorName: row.editor_name,
    latestRunId: row.latest_run_id,
    latestRunStatus: row.latest_run_status,
    description: row.description,
    inputNode: row.input_node,
    imageAttachmentId: row.image_attachment_id,
    archivedAt: row.archived_at,
    updatedById: row.updated_by_id,
    id: row.id,
    templateId: row.template_id,
    name: row.name,
    input: row.input,
    templateVersionId: row.template_version_id,
    createdById: row.created_by_id,
    createdAt: row.created_at,
    projectId: row.project_id,
    revision: row.revision,
    updatedAt: row.updated_at,
  }));
export const WorkflowInputInstanceListSchema = z.object({
  instances: z.array(WorkflowInputInstanceSchema),
});
export type WorkflowInputInstance = z.infer<typeof WorkflowInputInstanceSchema>;
export const WorkflowInstancePageSchema = z.object({
  instances: z.array(WorkflowInputInstanceSchema),
  total: z.number().int().nonnegative(),
});
export const WorkflowInstanceValidationSchema = z.object({
  ready: z.boolean(),
  problems: z.array(z.string()),
  revision: z.number().int().positive(),
});
export const WorkflowInstanceVersionSchema = z.object({
  id: z.string().min(1),
  version: z.number(),
  definition: WorkflowDefinitionSchema,
});
export type WorkflowInstanceFilters = {
  template_id?: string;
  search?: string;
  include_archived?: boolean;
  offset?: number;
  limit?: number;
};
export type RunWorkflowInstance = {
  script_step?: "clone" | "build" | "run";
  revision: number;
  mode: "saved" | "temporary" | "history";
  idempotency_key: string;
  input?: Record<string, string>;
  project_id?: string | null;
  image_attachment_id?: string;
  history_run_id?: string;
};
export type SaveWorkflowInputInstance = {
  description?: string;
  inputNode?: WorkflowNode | null;
  imageAttachmentId?: string | null;
  idempotencyKey?: string;
  name: string;
  input: Record<string, string>;
  projectId: string | null;
  revision?: number;
  templateVersionId?: string | null;
};

export const WorkflowInstanceRunListSchema = z.object({runs:z.array(WorkflowRunSchema),total:z.number().int().nonnegative()});

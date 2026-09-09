import { z } from "zod";

export const WorkflowInputInstanceSchema = z.object({
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
}).transform((row) => ({
  id: row.id, templateId: row.template_id, name: row.name, input: row.input,
  templateVersionId: row.template_version_id, createdById: row.created_by_id, createdAt: row.created_at,
  projectId: row.project_id, revision: row.revision, updatedAt: row.updated_at,
}));
export const WorkflowInputInstanceListSchema = z.object({ instances: z.array(WorkflowInputInstanceSchema) });
export type WorkflowInputInstance = z.infer<typeof WorkflowInputInstanceSchema>;
export type SaveWorkflowInputInstance = {
  name: string;
  input: Record<string, string>;
  projectId: string | null;
  revision?: number;
  templateVersionId?: string | null;
};
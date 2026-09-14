import { z } from "zod";
import {
  WorkflowDefinitionSchema,
  WorkflowRunDetailSchema,
  WorkflowRunSchema,
} from "./schemas";

export const WorkflowDebugLimitsSchema = z.object({
  max_attempts_per_node: z.number().int().positive(),
  max_rework_rounds: z.number().int().positive(),
  max_fan_out: z.number().int().positive(),
  max_duration_seconds: z.number().int().positive(),
  max_total_steps: z.number().int().positive(),
  max_cost_cents: z.number().int().positive(),
});
export type WorkflowDebugLimits = z.output<typeof WorkflowDebugLimitsSchema>;
export const WorkflowDebugPolicySchema = z
  .object({
    enabled: z.boolean(),
    user_active_runs: z.number().int().positive(),
    workspace_active_runs: z.number().int().positive(),
    user_starts_per_hour: z.number().int().positive(),
    max_duration_seconds: z.number().int().positive(),
    retention_seconds: z.number().int().positive(),
    payload_capacity_bytes: z.number().int().positive(),
  })
  .transform((p) => ({
    enabled: p.enabled,
    userActiveRuns: p.user_active_runs,
    workspaceActiveRuns: p.workspace_active_runs,
    userStartsPerHour: p.user_starts_per_hour,
    maxDurationSeconds: p.max_duration_seconds,
    retentionSeconds: p.retention_seconds,
    payloadCapacityBytes: p.payload_capacity_bytes,
  }));
export type WorkflowDebugPolicy = z.output<typeof WorkflowDebugPolicySchema>;
export const WorkflowDebugSettingsSchema = z.object({
  schema_version: z.literal("1"),
  revision: z.number().int().positive(),
  settings: WorkflowDebugPolicySchema,
});
export const WorkflowDebugCapabilitiesSchema = z
  .object({
    schema_version: z.literal("1"),
    enabled: z.boolean(),
    debug_policy_revision: z.number().int().positive(),
    default_limits: WorkflowDebugLimitsSchema,
    duration_ceiling_seconds: z.number().int().positive(),
    settings: WorkflowDebugPolicySchema,
    usage: z.object({
      workspace_active: z.number(),
      user_active: z.number(),
      user_hourly: z.number(),
      waiting_stop: z.number(),
    }),
    payload_bytes: z.number(),
  })
  .transform((v) => ({
    enabled: v.enabled,
    policyRevision: v.debug_policy_revision,
    defaultLimits: v.default_limits,
    durationCeilingSeconds: v.duration_ceiling_seconds,
    settings: v.settings,
    payloadBytes: v.payload_bytes,
    usage: {
      workspaceActive: v.usage.workspace_active,
      userActive: v.usage.user_active,
      userHourly: v.usage.user_hourly,
      waitingStop: v.usage.waiting_stop,
    },
  }));
export const WorkflowExecutionRefSchema = z
  .object({
    kind: z.literal("draft_test"),
    snapshot_id: z.string().uuid(),
    definition_hash: z.string().min(1),
    graph_schema_version: z.union([z.literal(1), z.literal(2)]),
    base_revision: z.number().int().positive(),
    base_draft_version_id: z.string().uuid().nullable(),
  })
  .transform((v) => ({
    kind: v.kind,
    snapshotId: v.snapshot_id,
    definitionHash: v.definition_hash,
    graphSchemaVersion: v.graph_schema_version,
    baseRevision: v.base_revision,
    baseDraftVersionId: v.base_draft_version_id,
  }));
const DebugMetadataSchema = z
  .object({
    effective_limits: WorkflowDebugLimitsSchema,
    deadline_at: z.string(),
    policy_revision: z.number(),
    retention_seconds: z.number(),
    purge_after: z.string().nullable(),
    payload_bytes: z.number(),
    cleanup_state: z.string(),
    details_purged_at: z.string().nullable(),
    purge_completed_at: z.string().nullable(),
    stop_requested_at: z.string().nullable(),
  })
  .transform((v) => ({
    effectiveLimits: v.effective_limits,
    deadlineAt: v.deadline_at,
    policyRevision: v.policy_revision,
    retentionSeconds: v.retention_seconds,
    purgeAfter: v.purge_after,
    payloadBytes: v.payload_bytes,
    cleanupState: v.cleanup_state,
    detailsPurgedAt: v.details_purged_at,
    purgeCompletedAt: v.purge_completed_at,
    stopRequestedAt: v.stop_requested_at,
  }));
export const WorkflowDebugRunSchema = z
  .object({
    schema_version: z.literal("1"),
    created: z.boolean(),
    run: WorkflowRunDetailSchema.refine(
      (r) =>
        z.string().uuid().safeParse(r.id).success &&
        r.status.length > 0 &&
        r.template_version_id === "" &&
        !r.issue_id &&
        !r.input_instance_id,
      "invalid draft trial identity",
    ),
    execution_ref: WorkflowExecutionRefSchema,
    debug: DebugMetadataSchema,
  })
  .transform((v) => ({
    created: v.created,
    run: v.run,
    executionRef: v.execution_ref,
    debug: v.debug,
  }));
export type WorkflowDebugRun = z.output<typeof WorkflowDebugRunSchema>;
export const WorkflowDebugDefinitionSchema = z
  .object({
    schema_version: z.literal("1"),
    execution_ref: WorkflowExecutionRefSchema,
    definition: WorkflowDefinitionSchema,
  })
  .transform((v) => ({
    executionRef: v.execution_ref,
    definition: v.definition,
  }));
export const WorkflowDebugListSchema = z.object({
  schema_version: z.literal("1"),
  total: z.number(),
  items: z.array(
    z
      .object({
        run: WorkflowRunSchema.refine(
          (r) =>
            z.string().uuid().safeParse(r.id).success &&
            r.status.length > 0 &&
            r.template_version_id === "" &&
            !r.issue_id &&
            !r.input_instance_id,
          "invalid draft trial identity",
        ),
        execution_ref: WorkflowExecutionRefSchema,
        cleanup_state: z.string(),
        details_purged_at: z.string().nullable(),
      })
      .transform((v) => ({
        run: v.run,
        executionRef: v.execution_ref,
        cleanupState: v.cleanup_state,
        detailsPurgedAt: v.details_purged_at,
      })),
  ),
});
export type StartWorkflowDebugRequest = {
  schema_version: "1";
  expected_revision: number;
  expected_debug_policy_revision: number;
  base_draft_version_id: string | null;
  definition: z.input<typeof WorkflowDefinitionSchema>;
  input: Record<string, unknown>;
  project_id: string | null;
  image_attachment_id: string | null;
  idempotency_key: string;
  execution_acknowledged: true;
};
export function workflowDebugPolicyWire(p: WorkflowDebugPolicy) {
  return {
    enabled: p.enabled,
    user_active_runs: p.userActiveRuns,
    workspace_active_runs: p.workspaceActiveRuns,
    user_starts_per_hour: p.userStartsPerHour,
    max_duration_seconds: p.maxDurationSeconds,
    retention_seconds: p.retentionSeconds,
    payload_capacity_bytes: p.payloadCapacityBytes,
  };
}

export const WorkflowDebugFailureSchema = z.object({
  code: z.string().optional(),
  diagnostics: z
    .array(
      z.object({
        code: z.string(),
        message: z.string(),
        field_path: z.string(),
        node_key: z.string().optional(),
        edge_id: z.string().optional(),
      }),
    )
    .optional(),
  dimensions: z
    .array(z.object({ code: z.string(), usage: z.number(), limit: z.number() }))
    .optional(),
  retry_after_seconds: z.number().optional(),
});

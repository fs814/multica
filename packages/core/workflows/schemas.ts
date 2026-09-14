import { z } from "zod";

/**
 * Wire contract for the workflow template control plane
 * (`/api/workflow-templates`).
 *
 * Two properties drive every choice in this file:
 *
 *  1. **Lenient enums.** `status`, node `type`, routing `strategy`,
 *     `on_failure`, and `join_policy` are all `z.string()` rather than
 *     `z.enum()`. The engine's node vocabulary is explicitly forward-looking -
 *     `condition`/`fan_out`/`join` are already part of the published graph
 *     contract even though only some have executors - so a server that starts
 *     emitting a node kind this client has never heard of must still render.
 *     An installed desktop build outlives any given server; a strict enum here
 *     would turn "server added a node type" into a blank Workflows page.
 *
 *  2. **A published version is immutable.** A template's `definition` is a
 *     snapshot, not a live object, so it is safe to cache and safe to degrade:
 *     when the graph fails to parse we fall back to an empty graph and still
 *     show the template's identity (name/status/version) instead of dropping
 *     the row entirely.
 *
 * `.loose()` everywhere lets additive server fields pass through unchanged
 * instead of being stripped, matching the rule used by
 * `packages/core/api/schemas.ts`. That is also why the shapes below are `type`
 * aliases rather than `interface`s: a `.loose()` schema's inferred type carries
 * an index signature, and only a type alias gets the implicit index signature
 * needed to stay assignable to it (an interface does not).
 */

// ---------------------------------------------------------------------------
// Graph: nodes, routing, limits
// ---------------------------------------------------------------------------

/** How an agent node picks the agent that runs it. */
export type WorkflowRouting = {
  /** "explicit" | "previous_step" | "capability" - kept open by design. */
  strategy: string;
  agent_id: string;
  from_node: string;
  capability: string;
  fallback_agent_id: string;
};

/** One condition edge. An empty `when_verdict` is the default branch. */
export type WorkflowPort = {
  id: string;
  type: string;
  required?: boolean;
  multiple?: boolean;
};
export type WorkflowDataEdge = {
  id: string;
  source: string;
  source_port: string;
  target: string;
  target_port: string;
  order: number;
};

export type WorkflowBranch = {
  id?: string;
  predicate?: { input_port: string; equals: unknown };
  when_verdict: string;
  target: string;
};

/**
 * One field an `input` node declares — what the Run dialog collects before the
 * first agent runs.
 *
 * The declaration lives on the graph rather than in the dialog because the graph
 * is the immutable, versioned artifact: a Run pinned to version 3 must stay
 * interpretable by version 3's field list forever. A dialog-side list would be
 * whatever the client shipped last.
 *
 * `type` is `string`, not a union of `"text" | "textarea" | "select"`, for the
 * same reason node `type` is (see the file header): the server owns the field
 * vocabulary and can widen it in any release, while an installed desktop build
 * keeps running. A strict union here would make "the server added a field kind"
 * a parse failure that costs the whole template detail page. The dialog narrows
 * it at render time and falls back to a text input for a kind it does not know.
 */
export type WorkflowInputField = {
  /** JSON key the submitted value is stored under in the run input bag. */
  key: string;
  /** What the human sees. Falls back to `key` when empty. */
  label: string;
  /** "text" | "textarea" | "select" — kept open by design. Empty means text. */
  type: string;
  required: boolean;
  /** Permitted values for a `select`. Ignored by the other kinds. */
  options: string[];
  /** Dialog-only hint text. Never sent to an agent. */
  placeholder: string;
};

export type WorkflowNode = {
  key: string;
  /** "agent" | "condition" | "fan_out" | "join" | "acceptance" | "end" | "input". */
  type: string;
  name: string;
  instruction: string;
  /** Outgoing edges. Edges live on the node so a dangling edge is local. */
  next: string[];
  next_ids?: string[];
  input_ports?: WorkflowPort[];
  output_ports?: WorkflowPort[];
  routing?: WorkflowRouting | null;
  submission_schema: string;
  acceptance_criteria: string[];
  /** "fail" | "block" | "rework". Empty means the server's default (block). */
  on_failure: string;
  /**
   * Where this node may send work back to. An empty list means rework is not
   * permitted from here - which is what makes the graph's cycles enumerable
   * rather than arbitrary, and why the UI can safely draw them.
   */
  rework_targets: string[];
  max_attempts: number;
  branches: WorkflowBranch[];
  join_policy: string;
  join_sources: string[];
  fan_out_max: number;
  /** "text" | "image" on input nodes. Absent means legacy text mode. */
  input_mode?: string;
  /** Stable attachment selected on an image-mode input node. */
  image_attachment_id?: string;
  /**
   * Declared intake fields. Only an `input` node carries them (the server's
   * validator rejects them elsewhere), and an empty list is legal — it means the
   * node documents where work enters without collecting anything typed, and the
   * dialog falls back to the freeform title/description pair.
   */
  input_fields: WorkflowInputField[];
};

export type WorkflowLimits = {
  max_attempts_per_node: number;
  max_rework_rounds: number;
  max_fan_out: number;
  max_duration_seconds: number;
  max_total_steps: number;
  max_cost_cents: number;
};

export type WorkflowDefinition = {
  schema_version: number;
  entry_node: string;
  nodes: WorkflowNode[];
  limits: WorkflowLimits;
  data_edges?: WorkflowDataEdge[];
};

const WorkflowRoutingSchema = z
  .object({
    strategy: z.string().optional().default(""),
    agent_id: z.string().optional().default(""),
    from_node: z.string().optional().default(""),
    capability: z.string().optional().default(""),
    fallback_agent_id: z.string().optional().default(""),
  })
  .loose();

const WorkflowPortSchema = z
  .object({
    id: z.string(),
    type: z.string(),
    required: z.boolean().optional(),
    multiple: z.boolean().optional(),
  })
  .loose();
const WorkflowDataEdgeSchema = z
  .object({
    id: z.string(),
    source: z.string(),
    source_port: z.string(),
    target: z.string(),
    target_port: z.string(),
    order: z.number().int(),
  })
  .loose();

const WorkflowBranchSchema = z
  .object({
    id: z.string().optional(),
    predicate: z
      .object({ input_port: z.string(), equals: z.unknown() })
      .optional(),
    when_verdict: z.string().optional().default(""),
    target: z.string().optional().default(""),
  })
  .loose();

const WorkflowInputFieldSchema = z
  .object({
    key: z.string(),
    label: z.string().optional().default(""),
    // Lenient, like every other enum here: an unknown field kind must render as
    // a plain text input, not fail the template's parse.
    type: z.string().optional().default(""),
    required: z.boolean().optional().default(false),
    options: z.array(z.string()).optional().default([]),
    placeholder: z.string().optional().default(""),
  })
  .loose();

export const WorkflowNodeSchema = z
  .object({
    key: z.string(),
    type: z.string(),
    name: z.string().optional().default(""),
    instruction: z.string().optional().default(""),
    next: z.array(z.string()).optional().default([]),
    next_ids: z.array(z.string()).optional(),
    input_ports: z.array(WorkflowPortSchema).optional(),
    output_ports: z.array(WorkflowPortSchema).optional(),
    // Only agent nodes carry routing; the server omits it elsewhere, and an
    // older/newer server may send null rather than omitting it.
    routing: WorkflowRoutingSchema.nullable().optional(),
    submission_schema: z.string().optional().default(""),
    acceptance_criteria: z.array(z.string()).optional().default([]),
    on_failure: z.string().optional().default(""),
    rework_targets: z.array(z.string()).optional().default([]),
    max_attempts: z.number().optional().default(0),
    branches: z.array(WorkflowBranchSchema).optional().default([]),
    join_policy: z.string().optional().default(""),
    join_sources: z.array(z.string()).optional().default([]),
    fan_out_max: z.number().optional().default(0),
    // Older input nodes omit this field and the editor treats absence as text.
    // Keep the wire value absent here rather than defaulting it: this schema is
    // shared by every node type, so a schema-level default would synthesize
    // input_mode on agent/condition/... nodes and the strict server validator
    // correctly rejects those nodes on the next save.
    input_mode: z.string().optional(),
    // The image is selected while authoring the node, ComfyUI-style. Persist
    // only the attachment id; previews derive a fresh download URL from it.
    image_attachment_id: z.string().optional().default(""),
    // The Go side tags this `omitempty`, so it is absent on every node that is
    // not an input node — including every node of every template published
    // before input nodes existed. Defaulting to `[]` rather than making it
    // optional keeps the editor's node shape total, so `node.input_fields` is
    // always an array and no caller has to guard.
    //
    // `.catch` and not just `.default`: a malformed field entry must cost the
    // declaration, not the whole template detail page. An empty list degrades to
    // the freeform dialog, which is the same behaviour as a template with no
    // input node — a graceful floor rather than a blank page.
    input_fields: z
      .array(WorkflowInputFieldSchema)
      .catch(() => [])
      .optional()
      .default([]),
  })
  .loose();

// Zero means "inherit the server default" for every limit: the Go side tags
// each bound `omitempty`, so an unset bound is simply absent from the JSON.
// The UI must therefore treat 0 as "server default", never as "no budget".
const zeroLimits = () => ({
  max_attempts_per_node: 0,
  max_rework_rounds: 0,
  max_fan_out: 0,
  max_duration_seconds: 0,
  max_total_steps: 0,
  max_cost_cents: 0,
});

const WorkflowLimitsSchema = z
  .object({
    max_attempts_per_node: z.number().optional().default(0),
    max_rework_rounds: z.number().optional().default(0),
    max_fan_out: z.number().optional().default(0),
    max_duration_seconds: z.number().optional().default(0),
    max_total_steps: z.number().optional().default(0),
    max_cost_cents: z.number().optional().default(0),
  })
  .loose();

export const WorkflowDefinitionSchema = z
  .object({
    // Defaults to the only schema version that exists today so a definition
    // written before the field was emitted still renders.
    schema_version: z.number().optional().default(1),
    data_edges: z.array(WorkflowDataEdgeSchema).optional(),
    entry_node: z.string().optional().default(""),
    nodes: z.array(WorkflowNodeSchema).optional().default([]),
    limits: WorkflowLimitsSchema.optional().default(zeroLimits),
  })
  .loose();

/** Graph shown when the server's definition is unreadable. */
const emptyDefinition = () => ({
  schema_version: 1,
  entry_node: "",
  nodes: [],
  limits: zeroLimits(),
});

// ---------------------------------------------------------------------------
// Template list / detail
// ---------------------------------------------------------------------------

/** One immutable version row of a template. */
export type WorkflowTemplateVersionSummary = {
  id: string;
  version: number;
  /** "draft" | "published" | "archived" - lenient, see file header. */
  status: string;
  published_at: string | null;
};

export type WorkflowTemplate = {
  id: string;
  workspace_id: string;
  key: string;
  name: string;
  description: string;
  /** "draft" | "published" | "archived" - lenient, see file header. */
  status: string;
  /** null until a version is published. */
  current_version: number | null;
  /** Built-in templates (e.g. `bug_fix`) are seeded, not user-authored. */
  is_builtin: boolean;
  node_count: number;
  /** Optimistic-concurrency token required by draft saves. */
  revision: number;
  created_at: string;
  updated_at: string;
};

export type WorkflowTemplateDetail = WorkflowTemplate & {
  definition: WorkflowDefinition;
  versions: WorkflowTemplateVersionSummary[];
};

export type WorkflowTemplateListResponse = {
  templates: WorkflowTemplate[];
  total: number;
};

/**
 * Node shape a *caller* may author. Only `key` and `type` are required; every
 * other field is optional, nested objects included, because the server fills
 * its own defaults (an empty `on_failure` means block, a zero `max_attempts`
 * inherits the graph limit, a routing without `agent_id` is fine for a
 * capability strategy). Requiring the full response shape here would force
 * every caller to spell out fields that only exist to be defaulted.
 */
export type WorkflowNodeInput = { key: string; type: string } & Partial<
  Omit<WorkflowNode, "key" | "type" | "routing" | "branches" | "input_fields">
> & {
    routing?: Partial<WorkflowRouting> | null;
    branches?: Partial<WorkflowBranch>[];
    /**
     * Partial for the same reason `branches` is: the server defaults an omitted
     * `type` to text, an omitted `required` to false, and an omitted `label` to
     * the key, so an author writing `{ key: "severity", type: "select", options:
     * [...] }` should not have to spell out `placeholder: ""`.
     */
    input_fields?: Partial<WorkflowInputField>[];
  };

/** Graph shape a caller may author. See {@link WorkflowNodeInput}. */
export type WorkflowDefinitionInput = {
  data_edges?: WorkflowDataEdge[];
  entry_node: string;
  nodes: WorkflowNodeInput[];
  schema_version?: number;
  limits?: Partial<WorkflowLimits>;
};

/** Body of `POST /api/workflow-templates`. The workspace is not in the body -
 *  it is resolved server-side from the `X-Workspace-Slug`/`X-Workspace-ID`
 *  header, exactly like the autopilot routes. */
export type CreateWorkflowTemplateRequest = {
  key: string;
  name: string;
  description?: string;
  definition: WorkflowDefinitionInput;
};

/**
 * Body of `PATCH /api/workflow-templates/{id}` - the editor's draft save.
 *
 * All three fields are independent and optional because the server treats an
 * *omitted* field as "leave it alone", not as "clear it". That distinction is
 * why this is not `Partial<CreateWorkflowTemplateRequest>`: `key` is absent by
 * design (it is an external identifier - plan section 9 resolves templates by
 * key for intake - and the server refuses to change it), so a `Partial` of the
 * create body would advertise a field the server ignores.
 *
 * Sending `{}` is a deliberate no-op that returns the current detail rather
 * than a 400, so an autosave that fires with nothing pending is harmless.
 */
export type UpdateWorkflowTemplateRequest = {
  name?: string;
  description?: string;
  definition?: WorkflowDefinitionInput;
  revision: number;
};

// ---------------------------------------------------------------------------
// Standalone graph validation
// ---------------------------------------------------------------------------

/**
 * Result of `POST /api/workflow-templates/validate`.
 *
 * `messages` are the server's own untranslated validation strings (e.g.
 * `Agent node "implement" must have exactly one outgoing edge, got 2`). They
 * are surfaced verbatim rather than mapped to i18n keys: the editor's
 * client-side mirror emits the same strings, so an author fixing an error must
 * not see two different descriptions of one problem depending on which side
 * reported it first.
 */
export const WorkflowDiagnosticSchema = z
  .object({
    code: z.string(),
    message: z.string(),
    field_path: z.string(),
    node_key: z.string().optional(),
    edge_id: z.string().optional(),
  })
  .loose()
  .transform(({ field_path, node_key, edge_id, ...rest }) => ({
    ...rest,
    fieldPath: field_path,
    nodeKey: node_key,
    edgeId: edge_id,
  }));
export type WorkflowDiagnostic = {
  code: string;
  message: string;
  fieldPath: string;
  nodeKey?: string;
  edgeId?: string;
};

export type WorkflowValidationResult = {
  diagnostics?: WorkflowDiagnostic[];
  valid: boolean;
  /** Non-empty whenever `valid` is false. Always an array, never null. */
  messages: string[];
};

/**
 * No `.default()` on either field - unlike every other schema in this file.
 *
 * Elsewhere a lenient default degrades a *description* of something (a name, a
 * status badge) and the worst case is a blander row. Here the payload IS a
 * verdict, so a default would fabricate one: `valid: z.boolean().default(true)`
 * would turn a reshaped response into "your graph is fine", and
 * `messages: [].default()` would turn it into "invalid, no reason given". Both
 * are worse than admitting we could not read the answer, so any drift fails the
 * parse and lands on {@link UNREADABLE_WORKFLOW_VALIDATION_RESULT}, which says
 * exactly that. Same reasoning as `CronPreviewResponseSchema`, where an
 * unreadable preview must not masquerade as "this cron never fires".
 *
 * `.loose()` still lets an additive field (a future per-node `field` pointer)
 * through untouched.
 */
export const WorkflowValidationResultSchema = z
  .object({
    valid: z.boolean(),
    messages: z.array(z.string()),
    diagnostics: z.array(WorkflowDiagnosticSchema).optional().catch(undefined),
  })
  .loose();

/**
 * Fallback for an unreadable validation response.
 *
 * `valid: false` is the safe direction: this endpoint writes nothing, so a
 * false negative costs the author one confusing line in the problems panel,
 * while a false positive would say "publishable" about a graph the server is
 * about to reject with a 422 - and publish is the irreversible step that pins a
 * graph into every future Run.
 *
 * The message is deliberately in the same untranslated register as the server's
 * own strings (see {@link WorkflowValidationResult}) so the panel can render
 * this entry through the same code path as a real validation error.
 */
export const UNREADABLE_WORKFLOW_VALIDATION_RESULT: WorkflowValidationResult = {
  valid: false,
  messages: [
    "the server's validation response could not be read; this graph was not checked",
  ],
};

const WorkflowTemplateVersionSummarySchema = z
  .object({
    id: z.string().optional().default(""),
    version: z.number().optional().default(0),
    status: z.string().optional().default(""),
    published_at: z.string().nullable().optional().default(null),
  })
  .loose();

export const WorkflowTemplateSchema = z
  .object({
    id: z.string(),
    workspace_id: z.string().optional().default(""),
    key: z.string().optional().default(""),
    name: z.string().optional().default(""),
    description: z.string().nullable().optional().default(""),
    status: z.string().optional().default("draft"),
    current_version: z.number().nullable().optional().default(null),
    is_builtin: z.boolean().optional().default(false),
    node_count: z.number().optional().default(0),
    revision: z.number().int().positive().optional().default(1),
    created_at: z.string().optional().default(""),
    updated_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowTemplateDetailSchema = z
  .object({
    ...WorkflowTemplateSchema.shape,
    // `.catch` (not just `.default`) is what makes the graph degrade
    // *locally*: a zod failure inside a nested object otherwise fails the whole
    // parse, which would cost the entire detail page over an unreadable graph.
    // The header (name/status/version history) does not depend on the
    // definition, so it must survive one - only the canvas degrades.
    definition: WorkflowDefinitionSchema.catch(emptyDefinition)
      .optional()
      .default(emptyDefinition),
    // Same reasoning for version history: a malformed version row costs the
    // history list, not the page.
    versions: z
      .array(WorkflowTemplateVersionSummarySchema)
      .catch(() => [])
      .optional()
      .default([]),
  })
  .loose();

export const WorkflowTemplateListResponseSchema = z
  .object({
    templates: z.array(WorkflowTemplateSchema).optional().default([]),
    total: z.number().optional().default(0),
  })
  .loose();

export const EMPTY_WORKFLOW_TEMPLATE_LIST_RESPONSE: WorkflowTemplateListResponse =
  {
    templates: [],
    total: 0,
  };

/**
 * Fallback detail. `id` is empty so a caller can tell "we could not read this
 * template" apart from a real one and skip navigation / show an error state -
 * the same convention `createAgentFromTemplate` uses. Client methods spread
 * this and overwrite `id` with the id from the URL the user clicked, so the
 * page header still makes sense after a parse miss.
 *
 * Because of that spread, `id` alone no longer distinguishes a parse miss from a
 * success on the by-id routes. `key` is the surviving signal: the server always
 * emits a non-empty key (it is a required, validated column), so an empty `key`
 * on a template the caller asked for by id can only mean the response was
 * unreadable - which is what the detail page gates its editor on. Never spread
 * a real `key` onto this fallback.
 */
export const EMPTY_WORKFLOW_TEMPLATE_DETAIL: WorkflowTemplateDetail = {
  id: "",
  workspace_id: "",
  key: "",
  name: "",
  description: "",
  status: "draft",
  current_version: null,
  is_builtin: false,
  node_count: 0,
  revision: 1,
  created_at: "",
  updated_at: "",
  definition: emptyDefinition(),
  versions: [],
};

// ---------------------------------------------------------------------------
// Runs: the execution side of a template
// ---------------------------------------------------------------------------

/**
 * A Run is an *execution* of one pinned template version, so every enum on it
 * is even more forward-looking than the template's: the engine owns the state
 * machine and can add a state (or a `source`, or a step `status`) in any server
 * release, while an installed desktop build keeps running. Hence `z.string()`
 * for `status` / `source` / `node_type` / `verdict` throughout - the runs list
 * showing an unfamiliar badge is a far better failure than a blank page.
 *
 * The one value this file refuses to invent is a submission `verdict`. The
 * server's own contract says prose never implies pass; a client that defaulted
 * a missing verdict to "pass" would break that rule from the other side. So the
 * verdict defaults to `""` (render as unknown) and an unreadable submission
 * degrades to `null` (render as "no submission"), never to a passing one.
 */

/** Summary row for the runs list. Enriched with the template's identity so the
 *  list renders without a second fetch per row. */
export type WorkflowRun = {
  input_instance_id?: string | null;
  input_instance_revision?: number | null;
  input_instance_name?: string | null;
  input_source?: string | null;
  id: string;
  workspace_id: string;
  /** null until/unless the run is attached to an issue. */
  issue_id: string | null;
  template_id: string;
  /** The immutable version whose graph this run executes. */
  template_version_id: string;
  /** "pending" | "running" | "blocked" | "completed" | "failed" | "cancelled" -
   *  lenient, see the section header. */
  status: string;
  /** "manual" | "autopilot" | "external" | "api" | "agent" - lenient. */
  source: string;
  source_event_id: string | null;
  accountable_user_id: string | null;
  /** Why the engine stopped short of a terminal state (e.g.
   *  `routing_no_candidate`, `submission_contract_invalid`). Server-side
   *  identifiers, not prose. */
  blocked_reason: string | null;
  failure_reason: string | null;
  failure_detail: string | null;
  started_at: string | null;
  completed_at: string | null;
  created_at: string;
  updated_at: string;
  template_name: string;
  template_key: string;
  step_count: number;
  /** The node the run is sitting on. null once the run is terminal. */
  current_node_key: string | null;
};

/** The structured block an agent returns to close a step. */
export type WorkflowSubmission = {
  raw_result?: string | null;
  /** "pass" | "fail" | "blocked" - lenient, and `""` when unreadable. Never
   *  defaulted to a passing value; see the section header. */
  verdict: string;
  /** Free-form `{type, summary, references}` bag. Kept open because the
   *  artifact shape is per-node contract, not per-engine. */
  artifact: Record<string, unknown>;
  rationale: string;
  confidence: number | null;
  root_cause: string | null;
  /** Present when the server rejected the submission against the node's
   *  schema. null (not `[]`) when there was nothing to report, so "not
   *  checked" stays distinguishable from "checked, clean". */
  validation_errors: string[] | null;
  submitted_at: string;
};

/** One attempt at one node. A rework round produces a *new* step with the same
 *  `node_key` and a higher `attempt`, which is what makes the trace a history
 *  rather than a mutable current-state list. */
export type WorkflowStep = {
  id: string;
  node_key: string;
  /** "agent" | "acceptance" | "end" | ... - lenient, see the file header. */
  node_type: string;
  attempt: number;
  /** "pending" | "queued" | "running" | "submitted" | "passed" | "failed" |
   *  "blocked" | "cancelled" - lenient. */
  status: string;
  agent_id: string | null;
  agent_name: string | null;
  /** The agent_task_queue row carrying this step's prompt, when routed. */
  task_id: string | null;
  /** The router's own explanation of why this agent (e.g. matched capability,
   *  reused previous step). Surfaced verbatim - it is the only record of a
   *  routing decision, and a run that picked the wrong specialist must be
   *  diagnosable after the fact. */
  routing_reason: string | null;
  failure_reason: string | null;
  failure_detail: string | null;
  started_at: string | null;
  completed_at: string | null;
  submission: WorkflowSubmission | null;
};

/** The open (or decided) human gate on a run. */
export type WorkflowAcceptance = {
  can_reject_without_rework?: boolean;
  id: string;
  step_id: string;
  /** "pending" | "accepted" | "rejected" - lenient. */
  status: string;
  reason: string | null;
  rework_target_node_key: string | null;
  /** Copied from the acceptance node so the reviewer sees the criteria the
   *  graph pinned, not whatever the template says today. */
  criteria: string[];
  /** The nodes a rejection may send work back to. An empty list means the
   *  reviewer can only accept - so the dialog must gate its reject action on
   *  this rather than assuming rework is always available. */
  rework_targets: string[];
  created_at: string;
};

export type WorkflowRunDetail = WorkflowRun & {
  /** The freeform run input (title / description / project_id today). A bag
   *  rather than a typed shape on purpose: typed input fields are a future
   *  additive change, and a client that pinned a schema here would reject the
   *  first run started with one. */
  input: Record<string, unknown>;
  steps: WorkflowStep[];
  acceptance: WorkflowAcceptance | null;
};

export type WorkflowRunListResponse = {
  runs: WorkflowRun[];
  total: number;
};

/**
 * Body of `POST /api/workflow-templates/{id}/run`.
 *
 * Interactive callers may omit `idempotency_key` and use the server-derived
 * double-click guard. Automation callers should persist and reuse a key so
 * retries through CLI, MCP, and HTTP converge on the same durable Run.
 */
export type RunWorkflowTemplateRequest = {
  title: string;
  description: string;
  idempotency_key?: string;
  /** Pins the published graph displayed when preparing this run. */
  templateVersionId?: string;
  /** Optional project for the issue the run creates. */
  project_id?: string | null;
};

/**
 * Body of `POST /api/workflow-runs/{id}/acceptance`.
 *
 * `reason` and `rework_target` are optional in the *type* but required by the
 * server when `accept` is false (and the target must be one of the acceptance's
 * `rework_targets`). Modelled this way because the two shapes are mutually
 * exclusive per `accept`, and a union here would force every caller to narrow
 * a value it just built.
 */
export type DecideWorkflowAcceptanceRequest = {
  accept: boolean;
  reason?: string;
  rework_target?: string;
};

export const WorkflowRunSchema = z
  .object({
    input_instance_id: z.string().nullable().optional(),
    input_instance_revision: z.number().nullable().optional(),
    input_instance_name: z.string().nullable().optional(),
    input_source: z.string().nullable().optional(),
    id: z.string(),
    workspace_id: z.string().optional().default(""),
    issue_id: z.string().nullable().optional().default(null),
    template_id: z.string().optional().default(""),
    template_version_id: z.string().optional().default(""),
    // No default: `status` is the field that tells a real run apart from an
    // unreadable one (see EMPTY_WORKFLOW_RUN_DETAIL), so it must never be
    // fabricated here. The server always emits it - it is a NOT NULL column.
    status: z.string(),
    source: z.string().optional().default(""),
    source_event_id: z.string().nullable().optional().default(null),
    accountable_user_id: z.string().nullable().optional().default(null),
    blocked_reason: z.string().nullable().optional().default(null),
    failure_reason: z.string().nullable().optional().default(null),
    failure_detail: z.string().nullable().optional().default(null),
    started_at: z.string().nullable().optional().default(null),
    completed_at: z.string().nullable().optional().default(null),
    created_at: z.string().optional().default(""),
    updated_at: z.string().optional().default(""),
    template_name: z.string().optional().default(""),
    template_key: z.string().optional().default(""),
    step_count: z.number().optional().default(0),
    current_node_key: z.string().nullable().optional().default(null),
  })
  .loose();

export const WorkflowSubmissionSchema = z
  .object({
    raw_result: z.string().nullable().optional().default(null),
    // Defaults to "" - the unknown verdict - never to "pass". See the section
    // header: the server's rule is that nothing but an explicit pass counts as
    // one, and a lenient default here would be a client-side way around it.
    verdict: z.string().optional().default(""),
    artifact: z
      .record(z.string(), z.unknown())
      .catch(() => ({}))
      .optional()
      .default(() => ({})),
    rationale: z.string().optional().default(""),
    confidence: z.number().nullable().optional().default(null),
    root_cause: z.string().nullable().optional().default(null),
    // `.catch(null)` keeps an unexpected element shape (a future
    // `{field, message}` object instead of a string) from failing the whole
    // submission, which would cost the reviewer the verdict too.
    validation_errors: z
      .array(z.string())
      .nullable()
      .catch(null)
      .optional()
      .default(null),
    submitted_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowStepSchema = z
  .object({
    id: z.string(),
    node_key: z.string().optional().default(""),
    node_type: z.string().optional().default(""),
    attempt: z.number().optional().default(0),
    status: z.string().optional().default(""),
    agent_id: z.string().nullable().optional().default(null),
    agent_name: z.string().nullable().optional().default(null),
    task_id: z.string().nullable().optional().default(null),
    routing_reason: z.string().nullable().optional().default(null),
    failure_reason: z.string().nullable().optional().default(null),
    failure_detail: z.string().nullable().optional().default(null),
    started_at: z.string().nullable().optional().default(null),
    completed_at: z.string().nullable().optional().default(null),
    // A structurally wrong submission degrades to null - "this step has no
    // submission" - rather than failing the step and erasing the row. That is
    // the honest reading: null says we have no verdict, which is true, whereas
    // dropping the step would hide that the node ran at all.
    submission: WorkflowSubmissionSchema.nullable()
      .catch(null)
      .optional()
      .default(null),
  })
  .loose();

export const WorkflowAcceptanceSchema = z
  .object({
    can_reject_without_rework: z.boolean().optional(),
    id: z.string(),
    step_id: z.string().optional().default(""),
    status: z.string().optional().default(""),
    reason: z.string().nullable().optional().default(null),
    rework_target_node_key: z.string().nullable().optional().default(null),
    criteria: z
      .array(z.string())
      .catch(() => [])
      .optional()
      .default([]),
    // Defaults to `[]`, which the reviewer UI must read as "accept only". An
    // unreadable list must not become a permissive one: sending a rework
    // target the graph does not allow is a 422, so guessing costs the reviewer
    // a failed submit instead of a disabled button.
    rework_targets: z
      .array(z.string())
      .catch(() => [])
      .optional()
      .default([]),
    created_at: z.string().optional().default(""),
  })
  .loose();

export const WorkflowRunDetailSchema = z
  .object({
    ...WorkflowRunSchema.shape,
    input: z
      .record(z.string(), z.unknown())
      .catch(() => ({}))
      .optional()
      .default(() => ({})),
    // Same local-degradation rule as the template's `definition`: the run
    // header (status, template, timing) does not depend on the trace, so one
    // malformed step must not cost the whole page. An empty trace on a run
    // with a non-zero `step_count` is the tell that this fired.
    steps: z
      .array(WorkflowStepSchema)
      .catch(() => [])
      .optional()
      .default([]),
    acceptance: WorkflowAcceptanceSchema.nullable()
      .catch(null)
      .optional()
      .default(null),
  })
  .loose();

export const WorkflowRunListResponseSchema = z
  .object({
    runs: z.array(WorkflowRunSchema).optional().default([]),
    total: z.number().optional().default(0),
  })
  .loose();

export const EMPTY_WORKFLOW_RUN_LIST_RESPONSE: WorkflowRunListResponse = {
  runs: [],
  total: 0,
};

/**
 * Fallback run detail.
 *
 * `status: ""` is the load-bearing field, and it is deliberately NOT the `id`.
 * The by-id client methods spread the requested id onto this fallback so the
 * page keeps its identity (breadcrumb, cancel target) after a parse miss -
 * which means `id` is non-empty on both a real run and a failed parse and
 * therefore cannot distinguish them. This mistake has already been made twice
 * in this feature: a run whose detail could not be read looked like a valid run
 * that simply had no steps, so the trace rendered "nothing happened yet" for a
 * run that was actually mid-flight.
 *
 * The server emits `status` for every run (NOT NULL column, and
 * `WorkflowRunSchema` has no default for it), so an empty `status` can only
 * mean the response was unreadable. Callers must gate on it before drawing a
 * trace or offering Cancel / Accept. Never spread a real `status` onto this.
 */
export const EMPTY_WORKFLOW_RUN_DETAIL: WorkflowRunDetail = {
  id: "",
  workspace_id: "",
  issue_id: null,
  template_id: "",
  template_version_id: "",
  status: "",
  source: "",
  source_event_id: null,
  accountable_user_id: null,
  blocked_reason: null,
  failure_reason: null,
  failure_detail: null,
  started_at: null,
  completed_at: null,
  created_at: "",
  updated_at: "",
  template_name: "",
  template_key: "",
  step_count: 0,
  current_node_key: null,
  input: {},
  steps: [],
  acceptance: null,
};

export type PublishWorkflowTemplateRequest = {
  revision: number;
  draft_version_id: string;
};

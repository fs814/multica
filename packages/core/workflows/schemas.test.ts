import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api/client";
import {
  WorkflowDefinitionSchema,
  WorkflowTemplateListResponseSchema,
  WorkflowValidationResultSchema,
  EMPTY_WORKFLOW_TEMPLATE_DETAIL,
  UNREADABLE_WORKFLOW_VALIDATION_RESULT,
} from "./schemas";

/**
 * Boundary-defense tests for the workflow template endpoints, mirroring
 * `packages/core/api/schema.test.ts`.
 *
 * The contract under test is not "the schema is correct" - it is "a response
 * this client did not anticipate degrades instead of throwing into React". The
 * workflow graph vocabulary is deliberately forward-looking (`condition`,
 * `fan_out`, and `join` are published contract ahead of their executors), and
 * an installed desktop build outlives any given server, so unknown enum values
 * are the *expected* case here, not an edge case.
 */

function stubFetchJson(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(typeof body === "string" ? body : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

const client = () => new ApiClient("https://api.example.test");

// A minimal but realistic Bug Fix graph: capability-routed analyze feeding a
// rework-capable implement node. Enough structure that the "unknown value"
// tests below are exercising a row the UI would actually render.
const bugFixDefinition = {
  schema_version: 1,
  entry_node: "analyze",
  nodes: [
    {
      key: "analyze",
      type: "agent",
      name: "Analyze",
      routing: { strategy: "capability", capability: "bug_analysis" },
      submission_schema: "analysis",
      next: ["implement"],
      on_failure: "block",
    },
    {
      key: "implement",
      type: "agent",
      name: "Implement",
      routing: { strategy: "capability", capability: "code_change" },
      submission_schema: "code_change",
      next: ["end"],
      on_failure: "rework",
      rework_targets: ["analyze"],
    },
    { key: "end", type: "end", name: "Done" },
  ],
  limits: { max_attempts_per_node: 3, max_rework_rounds: 3 },
};

const publishedTemplate = {
  id: "wft-1",
  workspace_id: "ws-1",
  key: "bug_fix",
  name: "Bug Fix",
  description: "Analyze, implement, validate, accept.",
  status: "published",
  current_version: 1,
  is_builtin: true,
  node_count: 3,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("listWorkflowTemplates", () => {
  it("falls back to an empty list when the body is null", async () => {
    stubFetchJson(null);
    const res = await client().listWorkflowTemplates();
    expect(res).toEqual({ templates: [], total: 0 });
  });

  it("falls back to an empty list when `templates` is not an array", async () => {
    stubFetchJson({ templates: "not-an-array", total: 3 });
    const res = await client().listWorkflowTemplates();
    expect(res).toEqual({ templates: [], total: 0 });
  });

  it("defaults a missing envelope to an empty list rather than throwing", async () => {
    // `{}` parses successfully because every field has a default - the point is
    // the caller gets a renderable shape, not a fallback object.
    stubFetchJson({});
    const res = await client().listWorkflowTemplates();
    expect(res).toEqual({ templates: [], total: 0 });
  });

  it("keeps a row whose status is a value this client has never seen", async () => {
    // Enum drift on the template lifecycle (e.g. a future `deprecated`). The
    // row must survive; the UI's `default` arm renders a generic badge.
    stubFetchJson({
      templates: [{ ...publishedTemplate, status: "deprecated" }],
      total: 1,
    });
    const res = await client().listWorkflowTemplates();
    expect(res.templates).toHaveLength(1);
    expect(res.templates[0]?.status).toBe("deprecated");
  });

  it("accepts an unpublished template with a null current_version", async () => {
    stubFetchJson({
      templates: [
        { ...publishedTemplate, status: "draft", current_version: null },
      ],
      total: 1,
    });
    const res = await client().listWorkflowTemplates();
    expect(res.templates[0]?.current_version).toBeNull();
  });

  it("accepts an older server row missing the derived node_count", async () => {
    const { node_count: _omit, ...withoutCount } = publishedTemplate;
    stubFetchJson({ templates: [withoutCount], total: 1 });
    const res = await client().listWorkflowTemplates();
    expect(res.templates[0]?.node_count).toBe(0);
    expect(res.templates[0]?.id).toBe("wft-1");
  });

  it("preserves unknown fields the schema didn't list", async () => {
    stubFetchJson({
      templates: [{ ...publishedTemplate, run_count: 42 }],
      total: 1,
    });
    const res = await client().listWorkflowTemplates();
    const row = res.templates[0] as unknown as Record<string, unknown>;
    expect(row.run_count).toBe(42);
  });

  it("hits the header-scoped collection route, not a workspace-scoped one", async () => {
    // Regression guard: the workspace rides on X-Workspace-Slug, so a
    // workspace id must never appear in the path.
    stubFetchJson({ templates: [], total: 0 });
    await client().listWorkflowTemplates();
    const url = String(vi.mocked(fetch).mock.calls[0]?.[0]);
    expect(url).toBe("https://api.example.test/api/workflow-templates");
  });
});

describe("getWorkflowTemplate", () => {
  it("falls back to a placeholder carrying the requested id", async () => {
    stubFetchJson({ wrong: "shape" });
    const detail = await client().getWorkflowTemplate("wft-1");
    expect(detail.id).toBe("wft-1");
    expect(detail.definition.nodes).toEqual([]);
    expect(detail.versions).toEqual([]);
  });

  it("falls back when the body is null", async () => {
    stubFetchJson(null);
    const detail = await client().getWorkflowTemplate("wft-1");
    expect(detail).toEqual({ ...EMPTY_WORKFLOW_TEMPLATE_DETAIL, id: "wft-1" });
  });

  it("parses a full graph with routing, rework targets and limits", async () => {
    stubFetchJson({
      ...publishedTemplate,
      definition: bugFixDefinition,
      versions: [
        {
          id: "v-1",
          version: 1,
          status: "published",
          published_at: "2026-07-01T00:00:00Z",
        },
      ],
    });
    const detail = await client().getWorkflowTemplate("wft-1");
    expect(detail.definition.entry_node).toBe("analyze");
    expect(detail.definition.nodes.map((n) => n.key)).toEqual([
      "analyze",
      "implement",
      "end",
    ]);
    expect(detail.definition.nodes[0]?.routing?.capability).toBe(
      "bug_analysis",
    );
    expect(detail.definition.nodes[1]?.rework_targets).toEqual(["analyze"]);
    expect(detail.definition.limits.max_rework_rounds).toBe(3);
    expect(detail.versions[0]?.published_at).toBe("2026-07-01T00:00:00Z");
  });

  it("keeps a node whose type has no executor in this client", async () => {
    // `fan_out` / `join` are contract before they are executable, and a newer
    // server may add more. Dropping the node would silently show a broken
    // graph, which is worse than rendering it with a generic glyph.
    stubFetchJson({
      ...publishedTemplate,
      definition: {
        ...bugFixDefinition,
        nodes: [
          ...bugFixDefinition.nodes,
          { key: "spread", type: "some_future_type", next: ["end"] },
        ],
      },
    });
    const detail = await client().getWorkflowTemplate("wft-1");
    expect(detail.definition.nodes).toHaveLength(4);
    expect(detail.definition.nodes[3]?.type).toBe("some_future_type");
  });

  it("keeps a node whose routing strategy is unknown", async () => {
    stubFetchJson({
      ...publishedTemplate,
      definition: {
        ...bugFixDefinition,
        nodes: [
          {
            key: "analyze",
            type: "agent",
            routing: { strategy: "some_future_strategy", capability: "x" },
            next: ["end"],
          },
          { key: "end", type: "end" },
        ],
      },
    });
    const detail = await client().getWorkflowTemplate("wft-1");
    expect(detail.definition.nodes[0]?.routing?.strategy).toBe(
      "some_future_strategy",
    );
  });

  it("degrades only the graph when the definition is malformed", async () => {
    // The header (name / status / version history) does not depend on the
    // graph, so an unreadable definition must not cost the whole page.
    stubFetchJson({
      ...publishedTemplate,
      definition: { nodes: "not-an-array" },
      versions: [
        { id: "v-1", version: 1, status: "published", published_at: null },
      ],
    });
    const detail = await client().getWorkflowTemplate("wft-1");
    expect(detail.name).toBe("Bug Fix");
    expect(detail.status).toBe("published");
    expect(detail.definition.nodes).toEqual([]);
    expect(detail.versions).toHaveLength(1);
  });

  it("fills node defaults so graph rendering never reads undefined", async () => {
    // A bare end node: the renderer maps over `next` / `rework_targets` /
    // `acceptance_criteria` unconditionally, so they must be arrays.
    stubFetchJson({
      ...publishedTemplate,
      definition: { entry_node: "end", nodes: [{ key: "end", type: "end" }] },
    });
    const node = (await client().getWorkflowTemplate("wft-1")).definition
      .nodes[0];
    expect(node?.next).toEqual([]);
    expect(node?.rework_targets).toEqual([]);
    expect(node?.acceptance_criteria).toEqual([]);
    expect(node?.branches).toEqual([]);
    expect(node?.on_failure).toBe("");
    expect(node?.max_attempts).toBe(0);
  });

  it("URL-encodes the template id", async () => {
    stubFetchJson(publishedTemplate);
    await client().getWorkflowTemplate("wft 1/2");
    const url = String(vi.mocked(fetch).mock.calls[0]?.[0]);
    expect(url).toContain("/api/workflow-templates/wft%201%2F2");
  });
});

describe("createWorkflowTemplate", () => {
  it("falls back to an empty detail so callers can skip navigation", async () => {
    // Mirrors createAgentFromTemplate: the template may well exist server-side,
    // but with an empty id the caller must not navigate to `/workflows/`.
    stubFetchJson({ unexpected: "shape" }, 201);
    const detail = await client().createWorkflowTemplate({
      key: "custom",
      name: "Custom",
      definition: bugFixDefinition,
    });
    expect(detail.id).toBe("");
    expect(detail.definition.nodes).toEqual([]);
  });

  it("returns the created detail on the happy path", async () => {
    stubFetchJson({ ...publishedTemplate, definition: bugFixDefinition }, 201);
    const detail = await client().createWorkflowTemplate({
      key: "bug_fix",
      name: "Bug Fix",
      definition: bugFixDefinition,
    });
    expect(detail.id).toBe("wft-1");
    expect(detail.definition.nodes).toHaveLength(3);
  });
});

describe("duplicateWorkflowTemplate", () => {
  it("falls back to an empty detail so callers do not navigate on malformed output", async () => {
    // The server may have committed the copy, but inventing an id would send the
    // user to the wrong template. The list invalidation lets them recover it.
    stubFetchJson({ unexpected: "shape" }, 201);
    const detail = await client().duplicateWorkflowTemplate("wft 1/2");
    expect(detail).toEqual(EMPTY_WORKFLOW_TEMPLATE_DETAIL);

    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    expect(String(url)).toBe(
      "https://api.example.test/api/workflow-templates/wft%201%2F2/duplicate",
    );
    expect(init).toMatchObject({ method: "POST" });
  });

  it("returns the new editable and runnable copy on the happy path", async () => {
    stubFetchJson(
      {
        ...publishedTemplate,
        id: "wft-copy",
        key: "bug_fix_copy",
        name: "Bug Fix Copy",
        status: "published",
        current_version: 1,
        is_builtin: false,
        definition: bugFixDefinition,
        versions: [
          {
            id: "v-copy",
            version: 1,
            status: "published",
            published_at: "2026-07-01T00:00:00Z",
          },
        ],
      },
      201,
    );
    const detail = await client().duplicateWorkflowTemplate("wft-1");
    expect(detail).toMatchObject({
      id: "wft-copy",
      key: "bug_fix_copy",
      status: "published",
      current_version: 1,
      is_builtin: false,
    });
  });
});

describe("publishWorkflowTemplate", () => {
  it("returns the detail with the new current_version", async () => {
    stubFetchJson({
      ...publishedTemplate,
      current_version: 2,
      definition: bugFixDefinition,
      versions: [
        {
          id: "v-1",
          version: 1,
          status: "archived",
          published_at: "2026-07-01T00:00:00Z",
        },
        {
          id: "v-2",
          version: 2,
          status: "published",
          published_at: "2026-07-02T00:00:00Z",
        },
      ],
    });
    const detail = await client().publishWorkflowTemplate("wft-1");
    expect(detail.current_version).toBe(2);
    expect(detail.versions).toHaveLength(2);
  });

  it("falls back to a placeholder carrying the id when unreadable", async () => {
    stubFetchJson({ wrong: "shape" });
    const detail = await client().publishWorkflowTemplate("wft-1");
    expect(detail.id).toBe("wft-1");
    expect(detail.current_version).toBeNull();
  });
});

describe("archiveWorkflowTemplate", () => {
  it("returns the archived summary", async () => {
    stubFetchJson({ ...publishedTemplate, status: "archived" });
    const tpl = await client().archiveWorkflowTemplate("wft-1");
    expect(tpl.status).toBe("archived");
    expect(tpl.id).toBe("wft-1");
  });

  it("falls back to an archived placeholder when unreadable", async () => {
    // The server already archived it; the fallback keeps the UI's state
    // consistent with that rather than pretending nothing happened.
    stubFetchJson(null);
    const tpl = await client().archiveWorkflowTemplate("wft-1");
    expect(tpl.id).toBe("wft-1");
    expect(tpl.status).toBe("archived");
  });
});

// Direct schema-level checks for invariants a client method can't express.
describe("workflow schemas", () => {
  it("preserves the image attachment selected on an input node", () => {
    const parsed = WorkflowDefinitionSchema.parse({
      entry_node: "intake",
      nodes: [
        {
          key: "intake",
          type: "input",
          input_mode: "image",
          image_attachment_id: "019ec09d-6222-722b-bdfa-427b105d80be",
          next: ["end"],
        },
        { key: "end", type: "end" },
      ],
    });
    expect(parsed.nodes[0]?.image_attachment_id).toBe(
      "019ec09d-6222-722b-bdfa-427b105d80be",
    );
  });

  it("treats an absent limits block as all-zero (inherit server defaults)", () => {
    const parsed = WorkflowDefinitionSchema.parse({
      entry_node: "end",
      nodes: [{ key: "end", type: "end" }],
    });
    expect(parsed.limits.max_attempts_per_node).toBe(0);
    expect(parsed.schema_version).toBe(1);
  });

  it("rejects a template row with no id (nothing to key a cache on)", () => {
    expect(
      WorkflowTemplateListResponseSchema.safeParse({
        templates: [{ name: "No id" }],
      }).success,
    ).toBe(false);
  });
});

describe("updateWorkflowTemplate", () => {
  it("PATCHes the by-id route with only the fields the caller supplied", async () => {
    // An omitted field means "leave it alone" server-side, so the client must not
    // helpfully fill in the ones the caller left out - sending `definition: null`
    // for a rename would clear the graph.
    stubFetchJson({ ...publishedTemplate, definition: bugFixDefinition });
    await client().updateWorkflowTemplate("wft-1", { name: "Renamed" });
    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    expect(String(url)).toBe(
      "https://api.example.test/api/workflow-templates/wft-1",
    );
    expect((init as RequestInit).method).toBe("PATCH");
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({
      name: "Renamed",
    });
  });

  it("returns the saved detail on the happy path", async () => {
    stubFetchJson({
      ...publishedTemplate,
      name: "Renamed",
      definition: bugFixDefinition,
      versions: [
        { id: "v-2", version: 2, status: "draft", published_at: null },
      ],
    });
    const detail = await client().updateWorkflowTemplate("wft-1", {
      name: "Renamed",
      definition: bugFixDefinition,
    });
    expect(detail.name).toBe("Renamed");
    expect(detail.definition.nodes).toHaveLength(3);
    // The draft the save created is visible in the history even though
    // `definition` still reports the published graph.
    expect(detail.versions[0]?.status).toBe("draft");
  });

  it("falls back to a placeholder carrying the requested id", async () => {
    stubFetchJson({ wrong: "shape" });
    const detail = await client().updateWorkflowTemplate("wft-1", {
      name: "Renamed",
    });
    expect(detail.id).toBe("wft-1");
    expect(detail.definition.nodes).toEqual([]);
  });

  it("falls back when the body is null", async () => {
    stubFetchJson(null);
    const detail = await client().updateWorkflowTemplate("wft-1", {
      name: "Renamed",
    });
    expect(detail).toEqual({ ...EMPTY_WORKFLOW_TEMPLATE_DETAIL, id: "wft-1" });
  });

  // The id is spread onto the fallback so the page keeps its identity, which
  // means `id` alone cannot distinguish a parse miss from a real save. `key` is
  // the surviving signal - the server always emits a non-empty key - and the
  // mutation's cache write gates on exactly this.
  it("leaves `key` empty on the fallback so a parse miss stays detectable", async () => {
    stubFetchJson({ wrong: "shape" });
    const missed = await client().updateWorkflowTemplate("wft-1", {
      name: "Renamed",
    });
    expect(missed.id).toBe("wft-1");
    expect(missed.key).toBe("");

    stubFetchJson({ ...publishedTemplate, definition: bugFixDefinition });
    const real = await client().updateWorkflowTemplate("wft-1", {
      name: "Renamed",
    });
    expect(real.key).toBe("bug_fix");
  });

  it("degrades only the graph when the saved definition comes back malformed", async () => {
    stubFetchJson({ ...publishedTemplate, definition: { nodes: 7 } });
    const detail = await client().updateWorkflowTemplate("wft-1", {
      name: "Bug Fix",
    });
    expect(detail.key).toBe("bug_fix");
    expect(detail.definition.nodes).toEqual([]);
  });

  it("URL-encodes the template id", async () => {
    stubFetchJson(publishedTemplate);
    await client().updateWorkflowTemplate("wft 1/2", { name: "x" });
    const url = String(vi.mocked(fetch).mock.calls[0]?.[0]);
    expect(url).toContain("/api/workflow-templates/wft%201%2F2");
  });
});

describe("validateWorkflowDefinition", () => {
  it("posts the graph to the collection-level validate route", async () => {
    // Must not be nested under an id: the graph under the author's cursor may
    // not correspond to anything stored yet.
    stubFetchJson({ valid: true, messages: [] });
    await client().validateWorkflowDefinition(bugFixDefinition);
    const [url, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    expect(String(url)).toBe(
      "https://api.example.test/api/workflow-templates/validate",
    );
    expect((init as RequestInit).method).toBe("POST");
    expect(JSON.parse(String((init as RequestInit).body))).toEqual({
      definition: bugFixDefinition,
    });
  });

  it("reports a valid graph with no messages", async () => {
    stubFetchJson({ valid: true, messages: [] });
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    expect(res).toEqual({ valid: true, messages: [] });
  });

  it("surfaces the server's messages verbatim on an invalid graph", async () => {
    // A 200 carrying valid:false is the *successful* answer here, so it must not
    // be confused with a transport failure. The strings are passed through
    // untranslated so they match the client-side mirror's wording exactly.
    stubFetchJson({
      valid: false,
      messages: [
        'Agent node "implement" must have exactly one outgoing edge, got 2',
        'entry_node "analyze" is not a declared node',
      ],
    });
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    expect(res.valid).toBe(false);
    expect(res.messages).toEqual([
      'Agent node "implement" must have exactly one outgoing edge, got 2',
      'entry_node "analyze" is not a declared node',
    ]);
  });

  // The whole point of this endpoint's fallback: "we could not read the answer"
  // must never be rendered as "publishable". Publish is the irreversible step
  // that pins a graph into every future Run, so the safe direction is invalid.
  it("treats a null body as invalid rather than valid", async () => {
    stubFetchJson(null);
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    expect(res.valid).toBe(false);
    expect(res.messages).toHaveLength(1);
    expect(res).toEqual(UNREADABLE_WORKFLOW_VALIDATION_RESULT);
  });

  it("treats a wrong-shaped body as invalid rather than valid", async () => {
    stubFetchJson({ ok: true, errors: [] });
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    expect(res.valid).toBe(false);
    expect(res).toEqual(UNREADABLE_WORKFLOW_VALIDATION_RESULT);
  });

  it("treats a missing `valid` field as invalid instead of defaulting it", async () => {
    // No `.default()` on `valid` on purpose: defaulting either way fabricates a
    // verdict. An absent field is drift, and drift means "not checked".
    stubFetchJson({ messages: [] });
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    expect(res.valid).toBe(false);
    expect(res).toEqual(UNREADABLE_WORKFLOW_VALIDATION_RESULT);
  });

  it("treats a truthy-but-not-boolean `valid` as invalid", async () => {
    // A stringly-typed `"true"` is exactly the drift that would sneak past a
    // coercing schema and green-light an unpublishable graph.
    stubFetchJson({ valid: "true", messages: [] });
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    expect(res.valid).toBe(false);
  });

  it("treats a null `messages` as invalid rather than an empty problem list", async () => {
    // valid:false with messages:null would otherwise render as "invalid, no
    // reason given", which the author cannot act on.
    stubFetchJson({ valid: false, messages: null });
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    expect(res).toEqual(UNREADABLE_WORKFLOW_VALIDATION_RESULT);
  });

  it("preserves an additive field the schema didn't list", async () => {
    // A future per-node `field` pointer must reach the panel unstripped.
    stubFetchJson({
      valid: false,
      messages: ["boom"],
      fields: ["nodes[1].next"],
    });
    const res = await client().validateWorkflowDefinition(bugFixDefinition);
    const row = res as unknown as Record<string, unknown>;
    expect(row.fields).toEqual(["nodes[1].next"]);
    expect(res.messages).toEqual(["boom"]);
  });
});

describe("workflow validation schema", () => {
  it("accepts a valid verdict with an empty message list", () => {
    const parsed = WorkflowValidationResultSchema.parse({
      valid: true,
      messages: [],
    });
    expect(parsed.valid).toBe(true);
  });

  it("rejects both fields being absent, so `{}` cannot read as valid", () => {
    expect(WorkflowValidationResultSchema.safeParse({}).success).toBe(false);
  });

  it("pins the unreadable fallback to invalid with an explanation", () => {
    // Guard against someone "simplifying" the fallback to `{valid:false,
    // messages:[]}`, which the problems panel would render as a blank success.
    expect(UNREADABLE_WORKFLOW_VALIDATION_RESULT.valid).toBe(false);
    expect(UNREADABLE_WORKFLOW_VALIDATION_RESULT.messages.length).toBe(1);
    expect(UNREADABLE_WORKFLOW_VALIDATION_RESULT.messages[0]).not.toBe("");
  });
});

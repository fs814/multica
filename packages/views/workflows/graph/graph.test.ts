import { describe, expect, it } from "vitest";
import type { WorkflowDefinition } from "@multica/core/workflows";
import { WorkflowDefinitionSchema } from "@multica/core/workflows";
import { bugFixDefinition } from "./bug-fix.fixture";
import { definitionToGraph } from "./to-graph";
import { graphToDefinition } from "./from-graph";
import { COL_GAP, ROW_GAP, autoLayout } from "./layout";
import { clientValidateGraph } from "./validate-graph";
import type { FlowEdge, FlowNode } from "./types";

// This module is the seam every part of the editor sits on: the canvas reads it,
// the properties panel writes through it, and 保存 sends whatever it produces to
// an endpoint that mints an immutable version. The tests below pin the two
// properties that make that safe - a lossless round trip, and a layout that
// treats rework edges as the backward edges the engine says they are - plus one
// case per rule the client validator claims to mirror.

const parse = (def: unknown): WorkflowDefinition =>
  WorkflowDefinitionSchema.parse(def);

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

describe("definitionToGraph / graphToDefinition round trip", () => {
  it("returns the real Bug Fix graph unchanged", () => {
    const def = bugFixDefinition();
    const { nodes, edges } = definitionToGraph(def);
    expect(graphToDefinition(nodes, edges, def)).toStrictEqual(def);
  });

  it("is byte-equivalent, not merely deep-equal", () => {
    // Deep equality would tolerate reordered keys and a reordered nodes[]. The
    // save endpoint mints a new immutable version from this JSON, so a diff that
    // churns on nothing but key order makes every real change unreviewable.
    const def = bugFixDefinition();
    const { nodes, edges } = definitionToGraph(def);
    expect(JSON.stringify(graphToDefinition(nodes, edges, def))).toBe(
      JSON.stringify(def),
    );
  });

  it("preserves fields the canvas never draws", () => {
    // The regression this guards is the expensive one: the canvas models edges
    // and node identity, so anything else is only preserved because from-graph
    // spreads the source node. If that spread is ever replaced by an explicit
    // field list, these are the semantics that vanish silently on first save.
    const def = bugFixDefinition();
    const { nodes, edges } = definitionToGraph(def);
    const round = graphToDefinition(nodes, edges, def);

    const acceptance = round.nodes.find((n) => n.key === "acceptance")!;
    expect(acceptance.acceptance_criteria).toEqual([
      "happy path verified",
      "edge case covered",
    ]);
    const validate = round.nodes.find((n) => n.key === "validate")!;
    expect(validate.submission_schema).toBe("test_report");
    expect(validate.routing).toEqual({
      strategy: "previous_step",
      agent_id: "",
      from_node: "implement",
      capability: "",
      fallback_agent_id: "",
    });
    expect(validate.instruction).not.toBe("");
    expect(round.limits.max_rework_rounds).toBe(3);
    expect(round.schema_version).toBe(1);
  });

  it("keeps join and fan-out semantics the first release does not render", () => {
    // fan_out/join runtime semantics survive editor round-trips,
    // so the editor must not be able to strip them by opening a graph.
    const def = parse({
      entry_node: "spread",
      nodes: [
        { key: "spread", type: "fan_out", next: ["work"], fan_out_max: 5 },
        {
          key: "work",
          type: "agent",
          next: ["gather"],
          routing: { strategy: "capability", capability: "code_change" },
          max_attempts: 7,
        },
        {
          key: "gather",
          type: "join",
          next: ["end"],
          join_policy: "rework",
          join_sources: ["work"],
          rework_targets: ["work"],
        },
        { key: "end", type: "end" },
      ],
    });
    const { nodes, edges } = definitionToGraph(def);
    const round = graphToDefinition(nodes, edges, def);

    expect(round).toStrictEqual(def);
    const gather = round.nodes.find((n) => n.key === "gather")!;
    expect(gather.join_policy).toBe("rework");
    expect(gather.join_sources).toEqual(["work"]);
    expect(round.nodes.find((n) => n.key === "spread")!.fan_out_max).toBe(5);
    expect(round.nodes.find((n) => n.key === "work")!.max_attempts).toBe(7);
  });

  it("cleans polluted input_mode fields from every non-input node type", () => {
    const def = parse({
      entry_node: "intake",
      nodes: [
        {
          key: "intake",
          type: "input",
          input_mode: "text",
          next: ["plan"],
        },
        {
          key: "plan",
          type: "agent",
          input_mode: "text",
          next: ["gate"],
          routing: { strategy: "capability", capability: "code_change" },
          submission_schema: "code_change",
        },
        {
          key: "gate",
          type: "condition",
          input_mode: "text",
          branches: [
            { when_verdict: "pass", target: "spread" },
            { when_verdict: "fail", target: "acceptance" },
          ],
        },
        {
          key: "spread",
          type: "fan_out",
          input_mode: "text",
          next: ["worker"],
          fan_out_max: 3,
        },
        {
          key: "worker",
          type: "agent",
          input_mode: "text",
          next: ["gather"],
          routing: { strategy: "capability", capability: "code_change" },
          submission_schema: "code_change",
        },
        {
          key: "gather",
          type: "join",
          input_mode: "text",
          next: ["acceptance"],
          join_policy: "fail_fast",
          join_sources: ["worker"],
        },
        {
          key: "acceptance",
          type: "acceptance",
          input_mode: "text",
          next: ["end"],
          rework_targets: ["plan"],
        },
        { key: "end", type: "end", input_mode: "text" },
      ],
    });
    const { nodes, edges } = definitionToGraph(def);
    const round = graphToDefinition(nodes, edges, def);

    expect(round.nodes.find((node) => node.type === "input")?.input_mode).toBe(
      "text",
    );
    for (const node of round.nodes.filter((node) => node.type !== "input")) {
      expect(node).not.toHaveProperty("input_mode");
    }
    expect(clientValidateGraph(round)).toEqual([]);
  });

  it("preserves forward-compatible keys a newer server added", () => {
    // The wire schema is `.loose()` so unknown keys pass through. An installed
    // build has no right to delete a field a newer server put on a node just
    // because it does not understand it.
    const def = parse({
      entry_node: "step",
      nodes: [
        {
          key: "step",
          type: "agent",
          next: ["end"],
          routing: { strategy: "capability", capability: "code_change" },
          future_field: { retries: 2 },
        },
        { key: "end", type: "end" },
      ],
      future_envelope_field: "keep me",
    });
    const { nodes, edges } = definitionToGraph(def);
    const round = graphToDefinition(nodes, edges, def);

    expect(round).toStrictEqual(def);
    // The extra keys survive at runtime but are absent from the hand-written
    // wire type by design (it enumerates what this build understands), so they
    // are read through a record view rather than with `any`.
    const asRecord = (value: unknown) => value as Record<string, unknown>;
    expect(asRecord(round.nodes[0]).future_field).toEqual({ retries: 2 });
    expect(asRecord(round).future_envelope_field).toBe("keep me");
  });

  it("survives a condition node's branches, defaults included", () => {
    const def = parse({
      entry_node: "check",
      nodes: [
        {
          key: "check",
          type: "condition",
          branches: [
            { when_verdict: "pass", target: "end" },
            { when_verdict: "fail", target: "fix" },
            // Empty verdict: the single default branch.
            { when_verdict: "", target: "review" },
          ],
        },
        {
          key: "fix",
          type: "agent",
          next: ["end"],
          routing: { strategy: "explicit", agent_id: "agent-1" },
        },
        {
          key: "review",
          type: "acceptance",
          next: ["end"],
          rework_targets: ["fix"],
        },
        { key: "end", type: "end" },
      ],
    });
    const { nodes, edges } = definitionToGraph(def);

    // Branch order carries a branch's index in branches[], so a round trip that
    // reordered them would change which branch the engine calls the default.
    const branchEdges = edges.filter((e) => e.data?.kind === "branch");
    expect(branchEdges.map((e) => e.data?.verdict)).toEqual([
      "pass",
      "fail",
      "",
    ]);
    expect(branchEdges.map((e) => e.data?.isDefaultBranch)).toEqual([
      false,
      false,
      true,
    ]);

    expect(graphToDefinition(nodes, edges, def)).toStrictEqual(def);
  });

  it("does not write canvas positions into the definition", () => {
    // A definition is the engine's contract; pixel coordinates are not part of
    // it. If a pan produced a new version, every reader of the version history
    // would have to guess which rows meant anything.
    const def = bugFixDefinition();
    const { nodes, edges } = definitionToGraph(def);
    const dragged = nodes.map((node) => ({
      ...node,
      position: { x: node.position.x + 37, y: node.position.y - 11 },
    }));
    expect(graphToDefinition(dragged, edges, def)).toStrictEqual(def);
  });

  it("ignores xyflow's node array reordering", () => {
    // xyflow raises a selected node above its siblings, mutating array order.
    // Node order must follow the definition so a click cannot produce a diff.
    const def = bugFixDefinition();
    const { nodes, edges } = definitionToGraph(def);
    const raised = [nodes[3]!, ...nodes.filter((_, i) => i !== 3)];
    expect(graphToDefinition(raised, edges, def)).toStrictEqual(def);
  });

  it("tags every edge kind and keeps rework edges distinguishable", () => {
    // Rework edges are the only cycles the server permits, so drawing one as an
    // ordinary edge would show the author a graph that looks illegal.
    const { edges } = definitionToGraph(bugFixDefinition());
    const rework = edges.filter((e) => e.data?.kind === "rework");
    expect(rework.map((e) => `${e.source}->${e.target}`)).toEqual([
      "implement->analyze",
      "validate->implement",
      "acceptance->analyze",
      "acceptance->implement",
      "acceptance->validate",
    ]);
    // Every rework edge runs backwards against the forward spine; none of them
    // may also appear as a `next` edge.
    const forward = new Set(
      edges
        .filter((e) => e.data?.kind === "next")
        .map((e) => `${e.source}->${e.target}`),
    );
    for (const edge of rework) {
      expect(forward.has(`${edge.source}->${edge.target}`)).toBe(false);
    }
    // Ids are unique even where two edges share a source and target, or xyflow
    // would drop the duplicate and hide it from the author.
    expect(new Set(edges.map((e) => e.id)).size).toBe(edges.length);
  });

  it("flags the entry node exactly once", () => {
    const { nodes } = definitionToGraph(bugFixDefinition());
    expect(nodes.filter((n) => n.data.isEntry).map((n) => n.id)).toEqual([
      "analyze",
    ]);
  });

  it("keeps a dangling edge instead of silently repairing it", () => {
    // The validator is about to complain about this edge; the canvas has to
    // agree with the complaint rather than quietly delete the evidence.
    const def = parse({
      entry_node: "step",
      nodes: [{ key: "step", type: "agent", next: ["ghost"] }],
    });
    const { nodes, edges } = definitionToGraph(def);
    expect(edges).toHaveLength(1);
    expect(edges[0]!.target).toBe("ghost");
    expect(graphToDefinition(nodes, edges, def).nodes[0]!.next).toEqual([
      "ghost",
    ]);
  });

  it("degrades an unknown node type to a renderer sentinel without losing it", () => {
    const def = parse({
      entry_node: "future",
      nodes: [
        { key: "future", type: "time_travel", next: ["end"] },
        { key: "end", type: "end" },
      ],
    });
    const { nodes, edges } = definitionToGraph(def);
    expect(nodes[0]!.type).toBe("unknown");
    // The original string is what gets written back: coercing it to a known kind
    // would let a save reshape semantics this build cannot even display.
    expect(nodes[0]!.data.node.type).toBe("time_travel");
    expect(graphToDefinition(nodes, edges, def)).toStrictEqual(def);
  });
});

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

const flowNode = (id: string): FlowNode => ({
  id,
  type: "agent",
  position: { x: 0, y: 0 },
  data: {
    id,
    nodeKey: id,
    type: "agent",
    isEntry: false,
    node: {
      key: id,
      type: "agent",
      name: "",
      instruction: "",
      next: [],
      submission_schema: "",
      acceptance_criteria: [],
      on_failure: "",
      rework_targets: [],
      max_attempts: 0,
      branches: [],
      join_policy: "",
      join_sources: [],
      fan_out_max: 0,
      input_fields: [],
    },
  },
});

const flowEdge = (
  source: string,
  target: string,
  kind: "next" | "branch" | "rework" = "next",
): FlowEdge => ({
  id: `${kind}:${source}->${target}`,
  source,
  target,
  type: kind,
  data: { kind, sourceKey: source, targetKey: target },
});

const xOf = (nodes: readonly FlowNode[]) =>
  Object.fromEntries(nodes.map((n) => [n.id, n.position.x]));

describe("autoLayout", () => {
  it("ranks by longest path over forward edges", () => {
    const nodes = ["a", "b", "c", "d"].map(flowNode);
    const edges = [
      flowEdge("a", "b"),
      flowEdge("b", "c"),
      flowEdge("c", "d"),
      // A shortcut must not pull `d` left: longest path, not shortest.
      flowEdge("a", "d"),
    ];
    expect(xOf(autoLayout(nodes, edges))).toEqual({
      a: 0,
      b: COL_GAP,
      c: 2 * COL_GAP,
      d: 3 * COL_GAP,
    });
  });

  it("ignores rework edges when ranking", () => {
    // This is the property that keeps layout terminating at all: the Bug Fix
    // graph's rework edges run backwards, so ranking them would make the rank
    // relation cyclic and the relaxation would never settle.
    const nodes = ["analyze", "implement", "validate", "acceptance", "end"].map(
      flowNode,
    );
    const forward = [
      flowEdge("analyze", "implement"),
      flowEdge("implement", "validate"),
      flowEdge("validate", "acceptance"),
      flowEdge("acceptance", "end"),
    ];
    const withRework = [
      ...forward,
      flowEdge("implement", "analyze", "rework"),
      flowEdge("validate", "implement", "rework"),
      flowEdge("acceptance", "analyze", "rework"),
      flowEdge("acceptance", "implement", "rework"),
      flowEdge("acceptance", "validate", "rework"),
    ];
    expect(xOf(autoLayout(nodes, withRework))).toEqual(
      xOf(autoLayout(nodes, forward)),
    );
    // And the spine really is a straight left-to-right chain.
    expect(xOf(autoLayout(nodes, withRework))).toEqual({
      analyze: 0,
      implement: COL_GAP,
      validate: 2 * COL_GAP,
      acceptance: 3 * COL_GAP,
      end: 4 * COL_GAP,
    });
  });

  it("is idempotent, so 自动布局 twice moves nothing", () => {
    const { nodes, edges } = definitionToGraph(bugFixDefinition());
    const once = autoLayout(nodes, edges);
    const twice = autoLayout(once, edges);
    expect(twice.map((n) => n.position)).toEqual(once.map((n) => n.position));
  });

  it("is deterministic across independent invocations", () => {
    const a = definitionToGraph(bugFixDefinition());
    const b = definitionToGraph(bugFixDefinition());
    expect(a.nodes.map((n) => n.position)).toEqual(
      b.nodes.map((n) => n.position),
    );
  });

  it("centers a fan-out layer about its spine", () => {
    const nodes = ["root", "one", "two", "three"].map(flowNode);
    const edges = [
      flowEdge("root", "one"),
      flowEdge("root", "two"),
      flowEdge("root", "three"),
    ];
    const laid = autoLayout(nodes, edges);
    const y = Object.fromEntries(laid.map((n) => [n.id, n.position.y]));
    expect(y).toEqual({
      root: 0,
      one: -ROW_GAP,
      two: 0,
      three: ROW_GAP,
    });
  });

  it("terminates on an illegal forward cycle rather than spinning", () => {
    // A forward cycle is rejected by the validator but perfectly constructible
    // on the canvas, and the author has to be able to see the mistake.
    const nodes = ["a", "b", "c"].map(flowNode);
    const edges = [flowEdge("a", "b"), flowEdge("b", "c"), flowEdge("c", "a")];
    const laid = autoLayout(nodes, edges);
    expect(laid).toHaveLength(3);
    for (const node of laid)
      expect(Number.isFinite(node.position.x)).toBe(true);
  });

  it("handles an empty graph", () => {
    expect(autoLayout([], [])).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Client validation
// ---------------------------------------------------------------------------

// Minimal legal graph to mutate one rule at a time. Kept as a factory so no
// case can leak state into the next.
const validDef = () =>
  parse({
    entry_node: "step",
    nodes: [
      {
        key: "step",
        type: "agent",
        next: ["review"],
        routing: { strategy: "capability", capability: "code_change" },
      },
      {
        key: "review",
        type: "acceptance",
        next: ["end"],
        rework_targets: ["step"],
      },
      { key: "end", type: "end" },
    ],
  });

const matching = (def: WorkflowDefinition, pattern: RegExp) =>
  clientValidateGraph(def).filter((message) => pattern.test(message));

describe("clientValidateGraph", () => {
  it("accepts the built-in Bug Fix graph", () => {
    // The seeded graph is published by the server on every boot; if the client
    // mirror rejects it, the mirror is wrong, not the graph.
    expect(clientValidateGraph(bugFixDefinition())).toEqual([]);
  });

  it("accepts the minimal graph the other cases mutate", () => {
    expect(clientValidateGraph(validDef())).toEqual([]);
  });

  it("never throws on a hostile graph", () => {
    // A crash in a validator would take down the editor over the exact graph the
    // author most needs to fix.
    expect(() => clientValidateGraph(parse({}))).not.toThrow();
    expect(clientValidateGraph(parse({}))).toEqual(["definition has no nodes"]);
  });

  it("stops at an unsupported schema_version", () => {
    // Node semantics may differ, so every other check would be unreliable.
    const problems = clientValidateGraph(
      parse({ ...validDef(), schema_version: 99 }),
    );
    expect(problems).toHaveLength(1);
    expect(problems[0]).toMatch(/unsupported schema_version 99/);
  });

  it("flags a duplicate node key", () => {
    const def = validDef();
    def.nodes.push({ ...def.nodes[0]! });
    expect(matching(def, /duplicate node key "step"/)).toHaveLength(1);
  });

  it("flags an empty node key", () => {
    const def = validDef();
    def.nodes[0]!.key = "";
    expect(matching(def, /node key is empty/)).toHaveLength(1);
  });

  it("flags an unknown node type", () => {
    const def = validDef();
    def.nodes[0]!.type = "time_travel";
    expect(matching(def, /unknown type "time_travel"/)).toHaveLength(1);
  });

  it("flags an entry_node that is not declared", () => {
    const def = validDef();
    def.entry_node = "nowhere";
    expect(
      matching(def, /entry_node "nowhere" is not a declared node/),
    ).toHaveLength(1);
  });

  it("flags an empty entry_node", () => {
    const def = validDef();
    def.entry_node = "";
    expect(matching(def, /entry_node is empty/)).toHaveLength(1);
  });

  it("flags a dangling edge", () => {
    const def = validDef();
    def.nodes[0]!.next = ["ghost"];
    expect(
      matching(def, /node "step" points at undeclared node "ghost"/),
    ).toHaveLength(1);
  });

  it("flags an empty edge target", () => {
    const def = validDef();
    def.nodes[0]!.next = [""];
    expect(matching(def, /node "step" has an empty edge target/)).toHaveLength(
      1,
    );
  });

  it("requires exactly one outgoing edge on an agent node", () => {
    const def = validDef();
    def.nodes[0]!.next = ["review", "end"];
    expect(
      matching(def, /Agent node "step" must have exactly one outgoing edge/),
    ).toHaveLength(1);
  });

  it("requires exactly one outgoing edge on an acceptance node", () => {
    const def = validDef();
    def.nodes[1]!.next = [];
    expect(
      matching(
        def,
        /Acceptance node "review" must have exactly one outgoing edge/,
      ),
    ).toHaveLength(1);
  });

  it("requires exactly one outgoing edge on a join node", () => {
    const def = parse({
      entry_node: "gather",
      nodes: [
        { key: "gather", type: "join", next: [], join_sources: ["end"] },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /Join node "gather" must have exactly one outgoing edge/),
    ).toHaveLength(1);
  });

  it("requires exactly one outgoing edge on a fan_out node", () => {
    const def = parse({
      entry_node: "spread",
      nodes: [
        { key: "spread", type: "fan_out", next: [] },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(
        def,
        /FanOut node "spread" must have exactly one outgoing edge \(the node to expand\)/,
      ),
    ).toHaveLength(1);
  });

  it("forbids an outgoing edge on an end node", () => {
    const def = validDef();
    def.nodes[2]!.next = ["step"];
    expect(
      matching(def, /End node "end" must not have outgoing edges/),
    ).toHaveLength(1);
  });

  it("forbids routing on an end node", () => {
    const def = validDef();
    def.nodes[2]!.routing = {
      strategy: "explicit",
      agent_id: "agent-1",
      from_node: "",
      capability: "",
      fallback_agent_id: "",
    };
    expect(
      matching(def, /End node "end" must not declare routing/),
    ).toHaveLength(1);
  });

  it("forbids routing on an acceptance node", () => {
    const def = validDef();
    def.nodes[1]!.routing = {
      strategy: "explicit",
      agent_id: "agent-1",
      from_node: "",
      capability: "",
      fallback_agent_id: "",
    };
    expect(
      matching(def, /Acceptance node "review" must not declare routing/),
    ).toHaveLength(1);
  });

  it("requires an acceptance node to declare a rework target", () => {
    const def = validDef();
    def.nodes[1]!.rework_targets = [];
    expect(
      matching(
        def,
        /Acceptance node "review" must declare at least one rework target/,
      ),
    ).toHaveLength(1);
  });

  it("requires rework targets when on_failure is rework", () => {
    const def = validDef();
    def.nodes[0]!.on_failure = "rework";
    expect(
      matching(
        def,
        /Agent node "step" has on_failure=rework but no rework_targets/,
      ),
    ).toHaveLength(1);
  });

  it("flags an unknown on_failure", () => {
    const def = validDef();
    def.nodes[0]!.on_failure = "explode";
    expect(
      matching(def, /node "step" has unknown on_failure "explode"/),
    ).toHaveLength(1);
  });

  it("flags an undeclared rework target", () => {
    const def = validDef();
    def.nodes[1]!.rework_targets = ["ghost"];
    expect(
      matching(def, /node "review" lists undeclared rework target "ghost"/),
    ).toHaveLength(1);
  });

  it("flags a self rework target", () => {
    // A self-rework would spin the same attempt without making progress.
    const def = validDef();
    def.nodes[1]!.rework_targets = ["review"];
    expect(
      matching(def, /node "review" lists itself as a rework target/),
    ).toHaveLength(1);
  });

  it("rejects a rework target that is not a reachable upstream node", () => {
    const def = validDef();
    def.nodes[1]!.rework_targets = ["end"];
    expect(
      matching(
        def,
        /rework target "end" that is not a reachable upstream node on a forward path/,
      ),
    ).toHaveLength(1);
  });

  it("requires a condition node to have branches", () => {
    const def = parse({
      entry_node: "check",
      nodes: [
        { key: "check", type: "condition", branches: [] },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /Condition node "check" has no branches/),
    ).toHaveLength(1);
  });

  it("permits at most one default branch", () => {
    const def = parse({
      entry_node: "check",
      nodes: [
        {
          key: "check",
          type: "condition",
          branches: [
            { when_verdict: "", target: "end" },
            { when_verdict: "", target: "end" },
          ],
        },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /Condition node "check" has more than one default branch/),
    ).toHaveLength(1);
  });

  it("flags an unknown branch verdict", () => {
    const def = parse({
      entry_node: "check",
      nodes: [
        {
          key: "check",
          type: "condition",
          branches: [{ when_verdict: "maybe", target: "end" }],
        },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(
        def,
        /Condition node "check" branches on unknown verdict "maybe"/,
      ),
    ).toHaveLength(1);
  });

  it("flags a branch with no target and a branch to an undeclared node", () => {
    const def = parse({
      entry_node: "check",
      nodes: [
        {
          key: "check",
          type: "condition",
          branches: [
            { when_verdict: "pass", target: "" },
            { when_verdict: "fail", target: "ghost" },
          ],
        },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /Condition node "check" has a branch with no target/),
    ).toHaveLength(1);
    expect(
      matching(
        def,
        /Condition node "check" branches to undeclared node "ghost"/,
      ),
    ).toHaveLength(1);
  });

  it("requires a join node to declare sources", () => {
    const def = parse({
      entry_node: "gather",
      nodes: [
        { key: "gather", type: "join", next: ["end"], join_sources: [] },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /Join node "gather" declares no join_sources/),
    ).toHaveLength(1);
  });

  it("flags an unknown join_policy and a rework policy with no targets", () => {
    const def = parse({
      entry_node: "gather",
      nodes: [
        {
          key: "gather",
          type: "join",
          next: ["end"],
          join_sources: ["end"],
          join_policy: "vibes",
        },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /Join node "gather" has unknown join_policy "vibes"/),
    ).toHaveLength(1);

    const reworkJoin = parse({
      entry_node: "gather",
      nodes: [
        {
          key: "gather",
          type: "join",
          next: ["end"],
          join_sources: ["end"],
          join_policy: "rework",
        },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(
        reworkJoin,
        /Join node "gather" has join_policy=rework but no rework_targets/,
      ),
    ).toHaveLength(1);
  });

  it("requires an agent node to declare routing", () => {
    const def = validDef();
    def.nodes[0]!.routing = null;
    expect(matching(def, /Agent node "step" declares no routing/)).toHaveLength(
      1,
    );
  });

  it("requires a strategy, and completeness per strategy", () => {
    const noStrategy = validDef();
    noStrategy.nodes[0]!.routing!.strategy = "";
    noStrategy.nodes[0]!.routing!.capability = "";
    expect(
      matching(noStrategy, /Agent node "step" declares no routing strategy/),
    ).toHaveLength(1);

    const explicit = validDef();
    explicit.nodes[0]!.routing = {
      strategy: "explicit",
      agent_id: "",
      from_node: "",
      capability: "",
      fallback_agent_id: "",
    };
    expect(
      matching(explicit, /uses explicit routing but sets no agent_id/),
    ).toHaveLength(1);

    const capability = validDef();
    capability.nodes[0]!.routing!.capability = "";
    expect(
      matching(capability, /uses capability routing but sets no capability/),
    ).toHaveLength(1);

    const previous = validDef();
    previous.nodes[0]!.routing = {
      strategy: "previous_step",
      agent_id: "",
      from_node: "",
      capability: "",
      fallback_agent_id: "",
    };
    expect(
      matching(previous, /uses previous_step routing but sets no from_node/),
    ).toHaveLength(1);

    const unknown = validDef();
    unknown.nodes[0]!.routing!.strategy = "telepathy";
    expect(
      matching(unknown, /unknown routing strategy "telepathy"/),
    ).toHaveLength(1);
  });

  it("rejects previous_step routing from a non-agent node", () => {
    // Only an Agent node has an Agent to inherit.
    const def = validDef();
    def.nodes[0]!.routing = {
      strategy: "previous_step",
      agent_id: "",
      from_node: "review",
      capability: "",
      fallback_agent_id: "",
    };
    expect(
      matching(def, /routes from "review", which is a acceptance node/),
    ).toHaveLength(1);
  });

  it("rejects previous_step routing from an undeclared node and from itself", () => {
    const undeclared = validDef();
    undeclared.nodes[0]!.routing = {
      strategy: "previous_step",
      agent_id: "",
      from_node: "ghost",
      capability: "",
      fallback_agent_id: "",
    };
    expect(
      matching(undeclared, /routes from undeclared node "ghost"/),
    ).toHaveLength(1);

    const itself = validDef();
    itself.nodes[0]!.routing = {
      strategy: "previous_step",
      agent_id: "",
      from_node: "step",
      capability: "",
      fallback_agent_id: "",
    };
    expect(
      matching(itself, /Agent node "step" routes from itself/),
    ).toHaveLength(1);
  });

  it("flags an unknown submission schema", () => {
    const def = validDef();
    def.nodes[0]!.submission_schema = "haiku";
    expect(
      matching(def, /references unknown submission schema "haiku"/),
    ).toHaveLength(1);
  });

  it("requires an End node to exist", () => {
    const def = parse({
      entry_node: "step",
      nodes: [
        {
          key: "step",
          type: "agent",
          next: ["step2"],
          routing: { strategy: "capability", capability: "x" },
        },
        {
          key: "step2",
          type: "agent",
          next: ["step"],
          routing: { strategy: "capability", capability: "x" },
        },
      ],
    });
    expect(
      matching(
        def,
        /definition has no End node, so a Run could never complete/,
      ),
    ).toHaveLength(1);
  });

  it("requires an End node reachable from the entry", () => {
    // An End that exists but cannot be reached is the subtler bug: the graph
    // looks complete and no Run can ever finish.
    const def = parse({
      entry_node: "loop_a",
      nodes: [
        {
          key: "loop_a",
          type: "agent",
          next: ["loop_b"],
          routing: { strategy: "capability", capability: "x" },
          rework_targets: [],
        },
        {
          key: "loop_b",
          type: "acceptance",
          next: ["loop_a"],
          rework_targets: ["loop_a"],
        },
        { key: "end", type: "end" },
      ],
    });
    const problems = clientValidateGraph(def);
    // Both the unreachable End and the forward cycle are real; the graph is a
    // single mistake but the author needs to see the cycle to fix it.
    expect(
      problems.filter((m) =>
        /no End node is reachable from entry_node/.test(m),
      ),
    ).toHaveLength(1);
  });

  it("flags an unreachable node", () => {
    const def = validDef();
    def.nodes.push({
      ...def.nodes[2]!,
      key: "orphan",
      type: "agent",
      next: ["end"],
      routing: {
        strategy: "capability",
        agent_id: "",
        from_node: "",
        capability: "x",
        fallback_agent_id: "",
      },
    });
    expect(
      matching(def, /node "orphan" is unreachable from entry_node "step"/),
    ).toHaveLength(1);
  });

  it("flags a forward cycle but not a rework cycle", () => {
    // The whole reason `EdgeKind` exists: rework_targets are the only legal
    // cycles, so the Bug Fix graph's loops must pass while a next/branch loop
    // must not.
    expect(
      matching(bugFixDefinition(), /non-rework cycle detected/),
    ).toHaveLength(0);

    const def = parse({
      entry_node: "a",
      nodes: [
        {
          key: "a",
          type: "agent",
          next: ["b"],
          routing: { strategy: "capability", capability: "x" },
        },
        {
          key: "b",
          type: "agent",
          next: ["a"],
          routing: { strategy: "capability", capability: "x" },
        },
        { key: "end", type: "end" },
      ],
    });
    expect(matching(def, /non-rework cycle detected: "b" -> "a"/)).toHaveLength(
      1,
    );
  });

  it("flags negative bounds but treats zero as the server default", () => {
    const negative = validDef();
    negative.limits.max_cost_cents = -1;
    negative.nodes[0]!.max_attempts = -2;
    expect(
      matching(negative, /limits\.max_cost_cents is negative/),
    ).toHaveLength(1);
    expect(
      matching(negative, /node "step" has negative max_attempts/),
    ).toHaveLength(1);

    // Every limit is zero in validDef(); that must stay silent, because zero
    // means "inherit the server default", never "no budget".
    expect(clientValidateGraph(validDef())).toEqual([]);
  });

  it("flags a negative fan_out_max", () => {
    const def = parse({
      entry_node: "spread",
      nodes: [
        { key: "spread", type: "fan_out", next: ["end"], fan_out_max: -1 },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /FanOut node "spread" has negative fan_out_max/),
    ).toHaveLength(1);
  });

  it("requires image-mode input nodes to carry the selected image", () => {
    const def = parse({
      entry_node: "intake",
      nodes: [
        {
          key: "intake",
          type: "input",
          input_mode: "image",
          next: ["end"],
        },
        { key: "end", type: "end" },
      ],
    });
    expect(
      matching(def, /image input node "intake" must select an image/),
    ).toHaveLength(1);

    def.nodes[0]!.image_attachment_id = "019ec09d-6222-722b-bdfa-427b105d80be";
    expect(clientValidateGraph(def)).toEqual([]);
  });

  it("suppresses reachability and cycle checks while edges are dangling", () => {
    // Running them on a malformed edge set produces messages that describe a
    // graph the author never wrote, burying the one real error.
    const def = validDef();
    def.nodes[0]!.next = ["ghost"];
    const problems = clientValidateGraph(def);
    expect(problems).toHaveLength(1);
    expect(problems[0]).toMatch(/points at undeclared node "ghost"/);
  });
});

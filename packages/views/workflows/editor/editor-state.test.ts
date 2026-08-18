import { describe, expect, it } from "vitest";
import type { WorkflowDefinition } from "@multica/core/workflows";
import { bugFixDefinition } from "../graph/bug-fix.fixture";
import {
  canRedo,
  canUndo,
  initialWorkflowEditorState,
  isDirty,
  selectedWorkflowNode,
  workflowEditorReducer,
  workingDefinition,
  type WorkflowEditorAction,
  type WorkflowEditorState,
} from "./editor-state";

/**
 * The reducer is where the editor's two most expensive bugs would live, so the
 * suite is organised around them rather than around the action list:
 *
 *  - **Opening a template and saving it must not change the graph.** A save mints
 *    an immutable version, so a lossy hydrate/read cycle would corrupt a template
 *    the moment someone looked at it. Proven end-to-end here (not only in the
 *    graph model) because the page reads through `workingDefinition`, and a
 *    reducer that stored a *projection* of the definition would pass the model's
 *    own round-trip test while still losing fields.
 *  - **A background refetch must not discard unsaved work.** React Query refetches
 *    on focus/reconnect and after every mutation, so this is not a rare race - it
 *    fires several times in a normal editing session.
 */

const TEMPLATE = "wft-1";

function seeded(definition: WorkflowDefinition = bugFixDefinition()) {
  return workflowEditorReducer(initialWorkflowEditorState(), {
    type: "hydrate",
    templateId: TEMPLATE,
    definition,
  });
}

function apply(
  state: WorkflowEditorState,
  ...actions: WorkflowEditorAction[]
): WorkflowEditorState {
  return actions.reduce(workflowEditorReducer, state);
}

// ---------------------------------------------------------------------------
// Round trip through the editor
// ---------------------------------------------------------------------------

describe("open and save with no edits", () => {
  it("returns the built-in Bug Fix graph byte-identically", () => {
    const def = bugFixDefinition();
    const state = seeded(def);

    // Deep equality first, so a failure names the field that changed.
    expect(workingDefinition(state)).toStrictEqual(def);
    // Then byte equality: the save endpoint stores these bytes as a version, and
    // a diff that churns on key order makes every real change unreviewable.
    expect(JSON.stringify(workingDefinition(state))).toBe(JSON.stringify(def));
  });

  it("reports the untouched template as not dirty", () => {
    // The consequence of the property above, and what keeps Save disabled on a
    // template nobody has edited. A projection-based reducer would report dirty
    // here even though nothing was touched.
    expect(isDirty(seeded())).toBe(false);
  });

  it("keeps the rework cycles and the fields no control renders", () => {
    const round = workingDefinition(seeded());
    const acceptance = round.nodes.find((node) => node.key === "acceptance")!;
    // Three rework targets: this is the bounded cycle set, the one thing the
    // server permits to make the graph cyclic.
    expect(acceptance.rework_targets).toEqual([
      "analyze",
      "implement",
      "validate",
    ]);
    expect(acceptance.acceptance_criteria).toEqual([
      "happy path verified",
      "edge case covered",
    ]);
    expect(round.limits.max_attempts_per_node).toBe(3);
  });
});

// ---------------------------------------------------------------------------
// Refetch clobber
// ---------------------------------------------------------------------------

describe("hydrate", () => {
  it("ignores a repeat hydrate of the same template, keeping unsaved edits", () => {
    const state = apply(
      seeded(),
      { type: "select", nodeId: "implement" },
      {
        type: "patch_node",
        node: {
          ...bugFixDefinition().nodes.find((node) => node.key === "implement")!,
          name: "Implement (edited)",
        },
      },
    );
    expect(isDirty(state)).toBe(true);

    // What a window-focus refetch does. The server's definition is the ORIGINAL,
    // so a reducer that re-seeded here would silently revert the edit above.
    const after = workflowEditorReducer(state, {
      type: "hydrate",
      templateId: TEMPLATE,
      definition: bugFixDefinition(),
    });

    expect(after).toBe(state);
    expect(
      after.present.nodes.find((node) => node.id === "implement")!.data.node
        .name,
    ).toBe("Implement (edited)");
    expect(isDirty(after)).toBe(true);
  });

  it("re-seeds for a different template", () => {
    // A different id is a different document. Not re-seeding here would show one
    // template's graph under another's name.
    const state = seeded();
    const other = workflowEditorReducer(state, {
      type: "hydrate",
      templateId: "wft-2",
      definition: {
        ...bugFixDefinition(),
        entry_node: "end",
        nodes: [bugFixDefinition().nodes.at(-1)!],
      },
    });
    expect(other.templateId).toBe("wft-2");
    expect(other.present.nodes.map((node) => node.id)).toEqual(["end"]);
    expect(other.past).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Dirty tracking
// ---------------------------------------------------------------------------

describe("dirty", () => {
  it("persists one deleted edge without removing nodes and reopens identically", () => {
    const opened = seeded();
    const removed = opened.present.edges.find(
      (edge) => edge.source === "analyze" && edge.target === "implement",
    );
    expect(removed).toBeDefined();
    expect(opened.present.edges.length).toBeGreaterThan(1);

    const edited = workflowEditorReducer(opened, {
      type: "set_graph",
      nodes: opened.present.nodes,
      edges: opened.present.edges.filter((edge) => edge.id !== removed?.id),
    });
    const payload = workingDefinition(edited);

    expect(payload.nodes.map((node) => node.key)).toEqual(
      bugFixDefinition().nodes.map((node) => node.key),
    );
    expect(payload.nodes.find((node) => node.key === "analyze")?.next).toEqual(
      [],
    );
    expect(isDirty(edited)).toBe(true);

    const saved = workflowEditorReducer(edited, {
      type: "mark_saved",
      definition: payload,
    });
    expect(isDirty(saved)).toBe(false);

    const reopened = workflowEditorReducer(initialWorkflowEditorState(), {
      type: "hydrate",
      templateId: TEMPLATE,
      definition: payload,
    });
    expect(workingDefinition(reopened)).toStrictEqual(payload);
    expect(reopened.present.edges.map((edge) => edge.id)).not.toContain(
      removed?.id,
    );
    expect(reopened.present.nodes).toHaveLength(opened.present.nodes.length);
  });

  it("repairs a polluted draft on save and reloads the clean definition", () => {
    const polluted = bugFixDefinition();
    for (const node of polluted.nodes) node.input_mode = "text";

    const opened = seeded(polluted);
    const payload = workingDefinition(opened);
    expect(isDirty(opened)).toBe(true);
    for (const node of payload.nodes) {
      expect(node).not.toHaveProperty("input_mode");
    }

    const saved = workflowEditorReducer(opened, {
      type: "mark_saved",
      definition: payload,
    });
    expect(isDirty(saved)).toBe(false);

    const reopened = workflowEditorReducer(initialWorkflowEditorState(), {
      type: "hydrate",
      templateId: TEMPLATE,
      definition: payload,
    });
    expect(isDirty(reopened)).toBe(false);
    expect(workingDefinition(reopened)).toStrictEqual(payload);
    for (const node of workingDefinition(reopened).nodes) {
      expect(node).not.toHaveProperty("input_mode");
    }
  });

  it("ignores canvas positions", () => {
    // Positions are not part of the definition (graph/from-graph.ts), so dragging
    // a card must not offer a save that would produce an empty diff.
    const state = seeded();
    const moved = workflowEditorReducer(state, {
      type: "set_graph",
      nodes: state.present.nodes.map((node) => ({
        ...node,
        position: { x: node.position.x + 40, y: node.position.y - 17 },
      })),
      edges: state.present.edges,
    });
    expect(isDirty(moved)).toBe(false);
    // ...and it must not consume an undo slot either, or undo would walk back
    // through drags before reaching a real edit.
    expect(canUndo(moved)).toBe(false);
  });

  it("clears after mark_saved and returns on the next edit", () => {
    const state = apply(seeded(), { type: "add_node", nodeType: "agent" });
    expect(isDirty(state)).toBe(true);

    const saved = workflowEditorReducer(state, {
      type: "mark_saved",
      definition: workingDefinition(state),
    });
    expect(isDirty(saved)).toBe(false);
    // The canvas is untouched by a save: the PATCH response's `definition` is the
    // template's *effective* graph, which after saving over a published template
    // is still the published bytes, so re-seeding from it would make the author's
    // own edit vanish.
    expect(saved.present).toBe(state.present);
    expect(canUndo(saved)).toBe(true);

    const again = workflowEditorReducer(saved, {
      type: "add_node",
      nodeType: "end",
    });
    expect(isDirty(again)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

describe("undo / redo", () => {
  it("reverts and reapplies a node edit", () => {
    const state = apply(seeded(), { type: "add_node", nodeType: "condition" });
    const withNode = workingDefinition(state);
    expect(withNode.nodes.map((node) => node.key)).toContain("condition_1");

    const undone = workflowEditorReducer(state, { type: "undo" });
    expect(workingDefinition(undone)).toStrictEqual(bugFixDefinition());
    expect(canRedo(undone)).toBe(true);

    const redone = workflowEditorReducer(undone, { type: "redo" });
    expect(JSON.stringify(workingDefinition(redone))).toBe(
      JSON.stringify(withNode),
    );
  });

  it("drops the redo branch once a new edit lands", () => {
    // Otherwise a later redo would splice an abandoned graph back in on top of
    // whatever the author has since built.
    const state = apply(
      seeded(),
      { type: "add_node", nodeType: "condition" },
      { type: "undo" },
      { type: "add_node", nodeType: "end" },
    );
    expect(canRedo(state)).toBe(false);
    expect(
      workingDefinition(state).nodes.map((node) => node.key),
    ).not.toContain("condition_1");
  });

  it("does not record auto-layout, because layout is not a definition change", () => {
    const state = apply(seeded(), { type: "auto_layout" });
    expect(canUndo(state)).toBe(false);
    expect(isDirty(state)).toBe(false);
  });

  it("is a no-op at the ends of the stack", () => {
    const state = seeded();
    expect(workflowEditorReducer(state, { type: "undo" })).toBe(state);
    expect(workflowEditorReducer(state, { type: "redo" })).toBe(state);
  });
});

// ---------------------------------------------------------------------------
// Adding nodes
// ---------------------------------------------------------------------------

describe("add_node", () => {
  it("never reuses a key, because keys are the graph's addressing scheme", () => {
    // A duplicate key would not create a second node - it would re-point another
    // node's edges at this one.
    const state = apply(
      seeded(),
      { type: "add_node", nodeType: "agent" },
      { type: "add_node", nodeType: "agent" },
      { type: "add_node", nodeType: "condition" },
    );
    const keys = workingDefinition(state).nodes.map((node) => node.key);
    expect(new Set(keys).size).toBe(keys.length);
    expect(keys).toContain("step_1");
    expect(keys).toContain("step_2");
    expect(keys).toContain("condition_1");
  });

  it("selects the new node and gives an agent node routing", () => {
    const state = workflowEditorReducer(seeded(), {
      type: "add_node",
      nodeType: "agent",
    });
    expect(state.selectedNodeId).toBe("step_1");
    // The validator rejects an agent node with no routing at all, so a blank one
    // arrives with the zero value rather than with the field missing.
    expect(selectedWorkflowNode(state)?.routing).toBeTruthy();
  });

  it("places the new node clear of the existing graph", () => {
    // Not at the origin and not auto-laid-out: an edgeless node ranks 0, so
    // layout would drop it on top of the entry node.
    const state = workflowEditorReducer(seeded(), {
      type: "add_node",
      nodeType: "agent",
    });
    const added = state.present.nodes.find((node) => node.id === "step_1")!;
    const rightmost = Math.max(
      ...state.present.nodes
        .filter((node) => node.id !== "step_1")
        .map((node) => node.position.x),
    );
    expect(added.position.x).toBeGreaterThan(rightmost);
  });
});

// ---------------------------------------------------------------------------
// JSON view
// ---------------------------------------------------------------------------

describe("apply_definition", () => {
  it("takes over limits, which no canvas control edits", () => {
    const def = bugFixDefinition();
    const state = workflowEditorReducer(seeded(def), {
      type: "apply_definition",
      definition: { ...def, limits: { ...def.limits, max_total_steps: 42 } },
    });
    expect(workingDefinition(state).limits.max_total_steps).toBe(42);
    expect(isDirty(state)).toBe(true);
  });

  it("drops a selection whose node the pasted graph no longer declares", () => {
    const def = bugFixDefinition();
    const state = apply(
      seeded(def),
      { type: "select", nodeId: "implement" },
      {
        type: "apply_definition",
        definition: {
          ...def,
          nodes: def.nodes.filter((n) => n.key !== "implement"),
        },
      },
    );
    expect(state.selectedNodeId).toBeNull();
    expect(selectedWorkflowNode(state)).toBeNull();
  });

  it("keeps a selection the pasted graph still declares", () => {
    const def = bugFixDefinition();
    const state = apply(
      seeded(def),
      { type: "select", nodeId: "implement" },
      { type: "apply_definition", definition: def },
    );
    expect(state.selectedNodeId).toBe("implement");
  });
});

// ---------------------------------------------------------------------------
// Panel edits to edge-derived fields
// ---------------------------------------------------------------------------

/**
 * `graphToDefinition` reads `branches` and `rework_targets` from the canvas EDGE
 * list, not from the node object, while the properties panel writes them onto the
 * node. So a `patch_node` carrying either field must also re-derive that node's
 * edges, or the edit is discarded on save - and silently, because `isDirty`
 * compares definitions: Save stays disabled while the panel still shows the
 * change.
 *
 * The rest of this suite is structurally blind to that: every other case
 * round-trips `definitionToGraph` output, where the node arrays and the edge list
 * agree by construction, and only ever patches `name`.
 */
describe("panel edits to edge-derived fields survive a save", () => {
  it("keeps a rework target added through the panel", () => {
    const def = bugFixDefinition();
    const opened = seeded(def);
    const validate = selectedWorkflowNode(
      workflowEditorReducer(opened, { type: "select", nodeId: "validate" }),
    );
    expect(validate).not.toBeNull();
    // Bug Fix ships validate with rework_targets ["implement"].
    expect(validate?.rework_targets).toEqual(["implement"]);

    const next = apply(opened, {
      type: "patch_node",
      node: { ...validate!, rework_targets: ["implement", "analyze"] },
    });

    const saved = workingDefinition(next).nodes.find(
      (n) => n.key === "validate",
    );
    expect(saved?.rework_targets).toEqual(["implement", "analyze"]);
    // The edit must also register as a change, or Save is disabled and the author
    // cannot persist what the panel is showing them.
    expect(isDirty(next)).toBe(true);
  });

  it("keeps an acceptance node's rework targets removed through the panel", () => {
    // The validator REQUIRES an acceptance node to declare at least one rework
    // target, so this field is the difference between a valid and an invalid
    // graph - losing an edit here means the author cannot fix a validation error.
    const def = bugFixDefinition();
    const opened = seeded(def);
    const acceptance = selectedWorkflowNode(
      workflowEditorReducer(opened, { type: "select", nodeId: "acceptance" }),
    );
    expect(acceptance?.rework_targets).toEqual([
      "analyze",
      "implement",
      "validate",
    ]);

    const next = apply(opened, {
      type: "patch_node",
      node: { ...acceptance!, rework_targets: ["implement"] },
    });

    const saved = workingDefinition(next).nodes.find(
      (n) => n.key === "acceptance",
    );
    expect(saved?.rework_targets).toEqual(["implement"]);
    expect(isDirty(next)).toBe(true);
  });

  it("keeps a condition branch retargeted through the panel", () => {
    // Bug Fix has no condition node, so build a minimal graph that does. Every
    // field the expansion reads must be present: the wire schema always supplies
    // `next` / `branches` / `rework_targets` as arrays, so a hand-built fixture
    // that omits them is testing a shape the app never sees.
    const base = bugFixDefinition();
    const def = {
      ...base,
      entry_node: "gate",
      nodes: [
        {
          key: "gate",
          type: "condition",
          next: [],
          rework_targets: [],
          branches: [
            { when_verdict: "pass", target: "done" },
            { when_verdict: "", target: "done" },
          ],
        },
        {
          key: "done",
          type: "end",
          next: [],
          branches: [],
          rework_targets: [],
        },
      ],
    } as unknown as WorkflowDefinition;

    const opened = seeded(def);
    const gate = selectedWorkflowNode(
      workflowEditorReducer(opened, { type: "select", nodeId: "gate" }),
    );
    expect(gate?.branches).toHaveLength(2);

    const next = apply(opened, {
      type: "patch_node",
      node: { ...gate!, branches: [{ when_verdict: "fail", target: "done" }] },
    });

    const saved = workingDefinition(next).nodes.find((n) => n.key === "gate");
    expect(saved?.branches).toHaveLength(1);
    expect(saved?.branches?.[0]?.when_verdict).toBe("fail");
    expect(isDirty(next)).toBe(true);
  });
});

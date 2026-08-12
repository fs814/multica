// @vitest-environment jsdom

/**
 * The intake card on the canvas.
 *
 * This node type exists on the canvas to answer one question - "what does this
 * workflow ask for?" - so the test asserts the declared field LABELS are on the
 * card. A card that rendered "3 fields" would render, would pass a smoke test that
 * only counted cards, and would answer nothing; that is the failure mode worth
 * guarding, not whether the component mounts.
 *
 * It renders the real `<ReactFlow />`, like workflow-canvas.test.tsx, because the
 * thing tsc cannot prove is that xyflow FINDS a renderer for the type string the
 * model writes. A missing `input` entry in the node registry drops the node
 * silently, which a reader would blame on their data.
 *
 * The EDGE layer is not asserted (jsdom has no layout, so xyflow never computes
 * edge geometry) - but the HANDLES are, because they are plain DOM on the card and
 * "the entry node has no incoming port" is a real claim about this node type.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import {
  WorkflowDefinitionSchema,
  type WorkflowDefinition,
} from "@multica/core/workflows";
import enCommon from "../../locales/en/common.json";
import enWorkflows from "../../locales/en/workflows.json";
import { definitionToGraph } from "../graph";
import { WorkflowCanvas } from "./workflow-canvas";

const TEST_RESOURCES = { en: { common: enCommon, workflows: enWorkflows } };

/** `count` declared fields, the first three named so they can be asserted. */
function intakeDefinition(extraFields: number = 0): WorkflowDefinition {
  const fields: unknown[] = [
    { key: "title", label: "Headline", type: "text", required: true },
    {
      key: "severity",
      label: "Severity",
      type: "select",
      required: true,
      options: ["low", "high"],
    },
    {
      key: "repro_steps",
      label: "Steps to reproduce",
      type: "textarea",
      required: false,
    },
  ];
  for (let index = 0; index < extraFields; index++) {
    fields.push({
      key: `extra_${index}`,
      label: `Extra ${index}`,
      type: "text",
      required: false,
    });
  }
  return WorkflowDefinitionSchema.parse({
    schema_version: 1,
    entry_node: "intake",
    nodes: [
      {
        key: "intake",
        type: "input",
        name: "Bug report",
        next: ["analyze"],
        input_fields: fields,
      },
      {
        key: "analyze",
        type: "agent",
        name: "Analyze",
        next: ["end"],
        routing: { strategy: "capability", capability: "bug_analysis" },
      },
      { key: "end", type: "end", name: "Done" },
    ],
  });
}

function renderCanvas(definition: WorkflowDefinition) {
  const { nodes, edges } = definitionToGraph(definition);
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <WorkflowCanvas
        nodes={nodes}
        edges={edges}
        selectedNodeId={null}
        readOnly={false}
        onNodesChange={vi.fn()}
        onEdgesChange={vi.fn()}
        onSelectNode={vi.fn()}
        onConnect={vi.fn()}
      />
    </I18nProvider>,
  );
}

/** The card xyflow drew for a node key, or null if it dropped the node. */
function card(nodeKey: string): Element | null {
  return document.querySelector(`[data-id="${nodeKey}"]`);
}

describe("input node card", () => {
  it("previews the image stored on an image-mode node", async () => {
    const definition = intakeDefinition();
    const intake = definition.nodes[0]!;
    intake.input_mode = "image";
    intake.image_attachment_id = "019ec09d-6222-722b-bdfa-427b105d80be";
    intake.input_fields = [];
    renderCanvas(definition);

    await screen.findByText("Selected image");
    // xyflow marks its node wrapper visibility:hidden until it measures layout
    // in jsdom, so role queries intentionally exclude the image. Query the
    // rendered element directly, as the existing card() helper does for handles.
    const image = document.querySelector<HTMLImageElement>(
      'img[alt="Workflow input image"]',
    );
    expect(image).not.toBeNull();
    expect(image).toHaveAttribute(
      "src",
      expect.stringContaining(
        "/api/attachments/019ec09d-6222-722b-bdfa-427b105d80be/download",
      ),
    );
  });

  it("is drawn at all, which the node registry has to make true", async () => {
    renderCanvas(intakeDefinition());

    // xyflow drops a node whose `type` has no renderer. The definition has three
    // nodes; a missing `input` entry would show two and still save three.
    expect(await screen.findByText("Bug report")).toBeInTheDocument();
    expect(card("intake")).not.toBeNull();
    expect(card("analyze")).not.toBeNull();
  });

  it("names its declared fields, which is the question this node exists to answer", async () => {
    renderCanvas(intakeDefinition());
    await screen.findByText("Bug report");

    // The LABELS, not a count: "where does the bug report go in?" is answered by
    // the field names, and before this node existed the answer was invisible on the
    // canvas because the Run dialog hardcoded it.
    expect(screen.getByText("Headline")).toBeInTheDocument();
    expect(screen.getByText("Severity")).toBeInTheDocument();
    expect(screen.getByText("Steps to reproduce")).toBeInTheDocument();
    // The kind, because "textarea" vs "select" is what tells an author whether the
    // step asks for prose or for one of a fixed set.
    expect(screen.getByText("select")).toBeInTheDocument();
    expect(screen.getByText("textarea")).toBeInTheDocument();
  });

  it("caps the list so a long form cannot push its neighbours off their row", async () => {
    // Cards are a fixed width in a layered layout, so an unbounded list would make
    // the graph unreadable. The tail says how many are hidden rather than silently
    // truncating - the properties panel is where the full list lives.
    renderCanvas(intakeDefinition(3));
    await screen.findByText("Bug report");

    expect(screen.getByText("Headline")).toBeInTheDocument();
    expect(screen.getByText("Extra 0")).toBeInTheDocument();
    expect(screen.queryByText("Extra 1")).toBeNull();
    expect(screen.getByText("+2 more fields")).toBeInTheDocument();
  });

  it("says so when the declaration is empty rather than reading as broken", async () => {
    const freeform = WorkflowDefinitionSchema.parse({
      schema_version: 1,
      entry_node: "intake",
      nodes: [
        // Named "Where work enters" rather than "Intake": the type badge already
        // reads "Intake", and a fixture that reused it would make a unique-match
        // assertion here an assertion about the fixture's naming.
        {
          key: "intake",
          type: "input",
          name: "Where work enters",
          next: ["end"],
        },
        { key: "end", type: "end", name: "Done" },
      ],
    });
    renderCanvas(freeform);
    await screen.findByText("Where work enters");

    // An empty declaration is legal: the Run dialog falls back to the freeform
    // pair, so the card states what will be collected instead of showing nothing.
    expect(screen.getByText("Title and description")).toBeInTheDocument();
  });

  it("has no incoming port, because nothing precedes where work enters", async () => {
    renderCanvas(intakeDefinition());
    await screen.findByText("Bug report");

    const intake = card("intake")!;
    // An input node must BE the entry (the validator rejects it anywhere else), so
    // a target port could only let an author build a graph the server refuses -
    // and would suggest a step can run before the human supplied the input.
    expect(intake.querySelectorAll(".react-flow__handle-left")).toHaveLength(0);
    expect(intake.querySelectorAll(".react-flow__handle-right")).toHaveLength(
      1,
    );
    // The agent node beside it still has both, so this is a property of the input
    // node and not of the card shell.
    expect(
      card("analyze")!.querySelectorAll(".react-flow__handle-left"),
    ).toHaveLength(1);
  });

  it("carries the entry badge and its own type label", async () => {
    renderCanvas(intakeDefinition());
    await screen.findByText("Bug report");

    expect(screen.getByText("Entry")).toBeInTheDocument();
    // "Intake" rather than "Input": the badge is product language, while the model's
    // type string stays `input`.
    expect(screen.getByText("Intake")).toBeInTheDocument();
  });
});

// @vitest-environment jsdom

/**
 * The add-node toolbar.
 *
 * One rule here is not expressible in a type and is the reason this file exists:
 * the server's validator permits AT MOST ONE input node per graph (a run has one
 * input bag and one entry node, so a second could only be dead), and a pill that
 * cheerfully creates a second one is a trap - the author designs the graph, the
 * editor accepts it, and the refusal arrives at publish time from the other end of
 * the page.
 *
 * The fixture for the disabled case is the seeded-shape graph WITH an intake node,
 * and the fixture for the enabled case is the REAL pre-input-node Bug Fix graph
 * (bug-fix.fixture.ts). Using a hand-written "graph with no input node" for the
 * second would let the two fixtures differ in something other than the input node,
 * which is the only thing the rule is about.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import {
  WorkflowDefinitionSchema,
  type WorkflowDefinition,
} from "@multica/core/workflows";
import enCommon from "../../locales/en/common.json";
import enWorkflows from "../../locales/en/workflows.json";
import { bugFixDefinition } from "../graph/bug-fix.fixture";
import { AddNodeToolbar } from "./add-node-toolbar";

const TEST_RESOURCES = { en: { common: enCommon, workflows: enWorkflows } };

/** The seeded Bug Fix shape after the intake node was added: entry is `intake`. */
function withIntake(): WorkflowDefinition {
  return WorkflowDefinitionSchema.parse({
    schema_version: 1,
    entry_node: "intake",
    nodes: [
      {
        key: "intake",
        type: "input",
        name: "Bug report",
        next: ["analyze"],
        input_fields: [
          { key: "title", label: "Title", type: "text", required: true },
        ],
      },
      {
        key: "analyze",
        type: "agent",
        next: ["end"],
        routing: { strategy: "capability", capability: "bug_analysis" },
      },
      { key: "end", type: "end" },
    ],
  });
}

function renderToolbar(
  definition: WorkflowDefinition,
  options: { readOnly?: boolean } = {},
) {
  const onAdd = vi.fn();
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <AddNodeToolbar
        readOnly={options.readOnly ?? false}
        definition={definition}
        onAdd={onAdd}
      />
    </I18nProvider>,
  );
  return { onAdd };
}

describe("AddNodeToolbar", () => {
  it("offers an Intake pill for a graph that has no input node", async () => {
    // The real pre-input-node Bug Fix graph: entry `analyze`, an agent node. This
    // is what every already-published template looks like, and adding an intake to
    // one is the whole upgrade path an author has.
    const { onAdd } = renderToolbar(bugFixDefinition());

    const pill = screen.getByRole("button", { name: "Add Intake step" });
    expect(pill).toBeEnabled();
    await userEvent.click(pill);
    // `input` is what the model calls the kind; the pill's LABEL is product
    // language and may change without the emitted type changing.
    expect(onAdd).toHaveBeenCalledWith("input");
  });

  it("disables the Intake pill once the graph has one, and says why", async () => {
    const { onAdd } = renderToolbar(withIntake());

    const pill = screen.getByRole("button", { name: "Add Intake step" });
    expect(pill).toBeDisabled();
    // The reason is on screen, not only in a `title` tooltip: a disabled button's
    // tooltip is unreachable by keyboard and unread by a screen reader, and "why is
    // this greyed out" is the only question the state raises.
    expect(
      screen.getByText(
        "This workflow already has an intake step. A run has one input, collected at the entry step, so only one is meaningful.",
      ),
    ).toBeInTheDocument();

    await userEvent.click(pill);
    // Disabled is not decoration: the click must not reach the reducer, because a
    // second input node is a graph the server refuses to publish.
    expect(onAdd).not.toHaveBeenCalled();
  });

  it("leaves every other pill enabled when an input node exists", () => {
    // The one-per-graph rule is about input nodes only. A toolbar that disabled the
    // whole row would read as "this graph is finished".
    renderToolbar(withIntake());

    for (const name of [
      "Add Issue step",
      "Add Acceptance step",
      "Add Condition step",
      "Add End step",
    ]) {
      expect(screen.getByRole("button", { name })).toBeEnabled();
    }
  });

  it("hides the whole row for a template the server will not let anyone edit", () => {
    // A built-in or published template answers PATCH with 409, so a row of dead
    // buttons would read as a broken editor rather than as a locked template. The
    // refusal reason must not leak out either - there is nothing to refuse.
    renderToolbar(withIntake(), { readOnly: true });

    expect(screen.queryByRole("button", { name: "Add Intake step" })).toBeNull();
    expect(screen.queryByText(/already has an intake step/)).toBeNull();
  });
});

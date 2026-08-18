import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { WorkflowDefinition, WorkflowNode } from "@multica/core/workflows";
import { agentListOptions } from "@multica/core/workspace/queries";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws1" }));

// Selector-form `t` against the real EN bundle, so a key this panel invents but
// never authors fails the test rather than silently rendering `undefined`.
vi.mock("../../i18n", async () => {
  const workflows = (await import("../../locales/en/workflows.json")).default;
  return {
    useT: () => ({
      t: (
        select: (bundle: Record<string, unknown>) => string,
        vars?: Record<string, string | number>,
      ) => {
        const raw = select(workflows as unknown as Record<string, unknown>);
        if (typeof raw !== "string") {
          throw new Error(`missing workflows key, got ${String(raw)}`);
        }
        return Object.entries(vars ?? {}).reduce(
          (text, [key, value]) => text.replaceAll(`{{${key}}}`, String(value)),
          raw,
        );
      },
    }),
  };
});

import { WorkflowPropertiesPanel } from "./properties-panel";

function node(
  patch: Partial<WorkflowNode> & Pick<WorkflowNode, "key" | "type">,
): WorkflowNode {
  return {
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
    ...patch,
  };
}

function definition(nodes: WorkflowNode[]): WorkflowDefinition {
  return {
    schema_version: 1,
    entry_node: nodes[0]?.key ?? "",
    nodes,
    limits: {
      max_attempts_per_node: 0,
      max_rework_rounds: 0,
      max_fan_out: 0,
      max_duration_seconds: 0,
      max_total_steps: 0,
      max_cost_cents: 0,
    },
  };
}

function renderPanel(
  selected: WorkflowNode | null,
  def: WorkflowDefinition,
  options: { readOnly?: boolean; agents?: unknown[] } = {},
) {
  const onChange = vi.fn();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData(
    agentListOptions("ws1").queryKey,
    (options.agents ?? []) as never,
  );
  render(
    <QueryClientProvider client={client}>
      <WorkflowPropertiesPanel
        node={selected}
        definition={def}
        readOnly={options.readOnly ?? false}
        onChange={onChange}
      />
    </QueryClientProvider>,
  );
  return { onChange };
}

describe("WorkflowPropertiesPanel", () => {
  it("says nothing is selected rather than rendering empty sections", () => {
    renderPanel(null, definition([]));
    expect(screen.getByText("No step selected")).toBeInTheDocument();
    expect(screen.queryByText("Basic info")).not.toBeInTheDocument();
  });

  it("edits the node through onChange without touching unrendered fields", async () => {
    const target = node({
      key: "implement",
      type: "agent",
      name: "Implement",
      // join_sources is not rendered for an agent node; it must survive an edit
      // to a field that is, or a save would silently drop graph semantics.
      join_sources: ["analyze"],
    });
    const { onChange } = renderPanel(target, definition([target]));

    await userEvent.type(screen.getByDisplayValue("Implement"), "!");

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "Implement!",
        join_sources: ["analyze"],
      }),
    );
  });

  it("renders the key as an uneditable value, since edges address nodes by it", () => {
    const target = node({ key: "implement", type: "agent" });
    renderPanel(target, definition([target]));

    expect(screen.getByText("implement")).toBeInTheDocument();
    // No textbox anywhere holds the key - the only inputs are name/instruction.
    for (const box of screen.getAllByRole("textbox")) {
      expect(box).not.toHaveValue("implement");
    }
  });

  it("offers no routing, prompt or failure controls on an End node", () => {
    const target = node({ key: "done", type: "end" });
    renderPanel(target, definition([target]));

    expect(screen.getByText("Nothing to configure")).toBeInTheDocument();
    expect(screen.queryByText("Agent routing")).not.toBeInTheDocument();
    expect(screen.queryByText("Prompt")).not.toBeInTheDocument();
    expect(screen.queryByText("On failure")).not.toBeInTheDocument();
  });

  it("refuses to edit a node kind this build does not understand", () => {
    const target = node({ key: "notify", type: "webhook" });
    renderPanel(target, definition([target]));

    expect(
      screen.getByText('Unsupported step type "webhook"'),
    ).toBeInTheDocument();
    expect(screen.queryByText("Prompt")).not.toBeInTheDocument();
    // Identity still renders, so the author can tell which node this is.
    expect(screen.getByText("notify")).toBeInTheDocument();
  });

  it("demands rework targets on an acceptance node, which the validator requires", async () => {
    const intake = node({ key: "intake", type: "input", next: ["implement"] });
    const implement = node({
      key: "implement",
      type: "agent",
      name: "Implement",
      next: ["review"],
    });
    const accept = node({ key: "review", type: "acceptance", next: ["end"] });
    const end = node({ key: "end", type: "end" });
    const orphan = node({ key: "orphan", type: "agent", next: ["end"] });
    const { onChange } = renderPanel(
      accept,
      definition([intake, implement, accept, end, orphan]),
    );

    expect(
      screen.getByText(
        "An acceptance step needs at least one target, so a rejection can route somewhere.",
      ),
    ).toBeInTheDocument();
    // The node itself is never a candidate: the validator rejects self-rework.
    const group = screen.getByRole("group", { name: "Rework targets" });
    expect(group).toHaveTextContent("Implement");
    expect(group).not.toHaveTextContent("intake");
    expect(group).not.toHaveTextContent("review");
    expect(group).not.toHaveTextContent("end");
    expect(group).not.toHaveTextContent("orphan");

    await userEvent.click(within(group).getByRole("checkbox"));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ rework_targets: ["implement"] }),
    );
  });

  it("explains how a new disconnected acceptance node gets eligible targets", () => {
    const step = node({ key: "step", type: "agent", next: ["end"] });
    const end = node({ key: "end", type: "end" });
    const accept = node({ key: "acceptance_1", type: "acceptance" });
    renderPanel(accept, definition([step, end, accept]));

    expect(
      screen.getByText(
        "Connect this step after an eligible upstream work step; it will appear here.",
      ),
    ).toBeInTheDocument();
  });

  it("only surfaces rework targets for on_failure=rework on an agent node", () => {
    const blocking = node({
      key: "implement",
      type: "agent",
      on_failure: "block",
    });
    const other = node({ key: "analyze", type: "agent" });
    renderPanel(blocking, definition([other, blocking]));

    expect(
      screen.queryByRole("group", { name: "Rework targets" }),
    ).not.toBeInTheDocument();
  });

  it("shows the resolved agent name and flags an archived one", () => {
    const target = node({
      key: "implement",
      type: "agent",
      routing: {
        strategy: "explicit",
        agent_id: "a1",
        from_node: "",
        capability: "",
        fallback_agent_id: "",
      },
    });
    renderPanel(target, definition([target]), {
      agents: [{ id: "a1", name: "Ada", archived_at: "2026-01-01T00:00:00Z" }],
    });

    expect(
      screen.getByRole("combobox", { name: "Agent that runs this step" }),
    ).toHaveTextContent("Ada (archived)");
  });

  it("keeps a pinned agent that is no longer in the workspace visible", () => {
    const target = node({
      key: "implement",
      type: "agent",
      routing: {
        strategy: "explicit",
        agent_id: "gone",
        from_node: "",
        capability: "",
        fallback_agent_id: "",
      },
    });
    renderPanel(target, definition([target]), { agents: [] });

    expect(
      screen.getByRole("combobox", { name: "Agent that runs this step" }),
    ).toHaveTextContent("Unknown agent gone");
  });

  it("renders only the field the chosen routing strategy makes mandatory", () => {
    const target = node({
      key: "implement",
      type: "agent",
      routing: {
        strategy: "capability",
        agent_id: "",
        from_node: "",
        capability: "code_change",
        fallback_agent_id: "",
      },
    });
    renderPanel(target, definition([target]));

    expect(screen.getByDisplayValue("code_change")).toBeInTheDocument();
    expect(
      screen.queryByRole("combobox", { name: "Agent that runs this step" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("combobox", { name: "Step to reuse the agent from" }),
    ).not.toBeInTheDocument();
  });

  it("names a condition node with no branches as a problem, not a hint", () => {
    const target = node({ key: "gate", type: "condition" });
    renderPanel(target, definition([target]));

    expect(
      screen.getByText("A condition step needs at least one branch."),
    ).toBeInTheDocument();
  });

  it("disables every control when the template cannot accept a draft edit", () => {
    const target = node({
      key: "implement",
      type: "agent",
      name: "Implement",
      instruction: "Fix it",
      routing: {
        strategy: "explicit",
        agent_id: "",
        from_node: "",
        capability: "",
        fallback_agent_id: "",
      },
    });
    renderPanel(target, definition([target]), { readOnly: true });

    for (const box of screen.getAllByRole("textbox")) {
      expect(box).toBeDisabled();
    }
    for (const combobox of screen.getAllByRole("combobox")) {
      expect(combobox).toBeDisabled();
    }
  });

  it("adds and removes acceptance criteria one line at a time", async () => {
    const target = node({
      key: "review",
      type: "acceptance",
      acceptance_criteria: ["Tests pass"],
      rework_targets: ["implement"],
    });
    const other = node({ key: "implement", type: "agent" });
    const { onChange } = renderPanel(target, definition([other, target]));

    await userEvent.click(
      screen.getByRole("button", { name: "Add criterion" }),
    );
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ acceptance_criteria: ["Tests pass", ""] }),
    );

    onChange.mockClear();
    await userEvent.click(
      screen.getByRole("button", { name: "Remove criterion 1" }),
    );
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ acceptance_criteria: [] }),
    );
  });
});

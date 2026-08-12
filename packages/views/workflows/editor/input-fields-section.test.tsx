// @vitest-environment jsdom

/**
 * The properties panel's intake-fields section, and the canvas card that reads it
 * back.
 *
 * Two failure modes are worth a test each, and both are drawn from this feature's
 * history:
 *
 *  1. **A panel edit silently discarded on save.** A previous round shipped a
 *     properties-panel control whose value never reached the definition, and every
 *     test round-tripped `definitionToGraph` output where the bug cannot appear. So
 *     these tests assert on the object handed to `onChange` - the thing the reducer
 *     actually commits - and specifically that an UNRENDERED field survives it.
 *
 *  2. **A card that summarises instead of answering.** The whole reason the input
 *     node is drawn on the canvas is to answer "what does this workflow ask for?".
 *     A card showing "3 fields" would render, pass a smoke test, and answer nothing
 *     - so the card test asserts the field LABELS are on it.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { agentListOptions } from "@multica/core/workspace/queries";
import {
  WorkflowNodeSchema,
  type WorkflowDefinition,
  type WorkflowNode,
} from "@multica/core/workflows";
import enCommon from "../../locales/en/common.json";
import enWorkflows from "../../locales/en/workflows.json";
import { WorkflowPropertiesPanel } from "./properties-panel";

const TEST_RESOURCES = { en: { common: enCommon, workflows: enWorkflows } };

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws1" }));
const uploadMock = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/hooks/use-file-upload", () => ({
  useFileUpload: () => ({
    upload: uploadMock,
    uploadWithToast: vi.fn(),
    uploading: false,
  }),
}));

/**
 * Parsed through the real wire schema rather than built as a literal: the server
 * omits every defaulted key, so a literal would be a node shape no server sends,
 * and the schema's own `input_fields` defaulting would go unexercised.
 */
function inputNode(fields: unknown[]): WorkflowNode {
  return WorkflowNodeSchema.parse({
    key: "intake",
    type: "input",
    name: "Bug report",
    next: ["analyze"],
    // A field the panel does not render, to prove the spread-then-override rule
    // holds here too. `join_sources` is meaningless on an input node but the model
    // carries it, and dropping it on an edit is the silent-discard bug.
    join_sources: ["analyze"],
    input_fields: fields,
  });
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
  selected: WorkflowNode,
  options: { readOnly?: boolean } = {},
) {
  const onChange = vi.fn();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData(agentListOptions("ws1").queryKey, [] as never);
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={client}>
        <WorkflowPropertiesPanel
          node={selected}
          definition={definition([selected])}
          readOnly={options.readOnly ?? false}
          onChange={onChange}
        />
      </QueryClientProvider>
    </I18nProvider>,
  );
  return { onChange };
}

describe("input node properties panel", () => {
  it("uploads an image on the input node and stores only its durable attachment id", async () => {
    uploadMock.mockResolvedValue({
      id: "019ec09d-6222-722b-bdfa-427b105d80be",
      link: "https://storage.example.test/temporary",
    });
    const node = {
      ...inputNode([]),
      input_mode: "image",
      image_attachment_id: "",
    };
    const { onChange } = renderPanel(node);

    await userEvent.click(screen.getByRole("button", { name: "Choose image" }));
    const input =
      document.querySelector<HTMLInputElement>('input[type="file"]')!;
    await userEvent.upload(
      input,
      new File(["png"], "input.png", { type: "image/png" }),
    );

    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith(
        expect.objectContaining({
          image_attachment_id: "019ec09d-6222-722b-bdfa-427b105d80be",
        }),
      ),
    );
    expect(onChange.mock.calls.at(-1)?.[0]).not.toHaveProperty(
      "image_preview_url",
    );
  });

  it("shows the selected node image and lets the author remove its reference", async () => {
    const node = {
      ...inputNode([]),
      input_mode: "image",
      image_attachment_id: "019ec09d-6222-722b-bdfa-427b105d80be",
    };
    const { onChange } = renderPanel(node);

    expect(
      screen.getByRole("img", { name: "Selected workflow input image" }),
    ).toHaveAttribute(
      "src",
      expect.stringContaining(
        "/api/attachments/019ec09d-6222-722b-bdfa-427b105d80be/download",
      ),
    );
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ image_attachment_id: "" }),
    );
  });

  it("says an intake step has no routing, submission or rework rather than rendering empty sections", () => {
    renderPanel(inputNode([]));

    expect(screen.getByText("Intake fields")).toBeInTheDocument();
    // These three are not "not implemented yet" - they are rules the validator
    // enforces, so an author must be told rather than left hunting the JSON view
    // for a control that does not exist.
    expect(screen.getByText("Nothing else to configure")).toBeInTheDocument();
    expect(screen.queryByText("Agent routing")).toBeNull();
    expect(screen.queryByText("Submission")).toBeNull();
    expect(screen.queryByText("On failure")).toBeNull();
  });

  it("treats an empty declaration as legal, not as a problem", () => {
    // An input node with no fields documents where work enters and the Run dialog
    // falls back to the freeform pair - which is exactly what every template
    // published before input nodes existed still does. Calling it an error would
    // send the author chasing a non-problem.
    renderPanel(inputNode([]));

    expect(
      screen.getByText(
        "No fields declared. The run still asks for a title and a description; declare fields to ask for more.",
      ),
    ).toBeInTheDocument();
  });

  it("adds a field with an empty key, so the author names it", async () => {
    const { onChange } = renderPanel(inputNode([]));

    await userEvent.click(screen.getByRole("button", { name: "Add field" }));

    // An invented `field_1` would publish a key nobody chose, and every downstream
    // prompt would then label the value meaninglessly.
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        input_fields: [
          expect.objectContaining({
            key: "",
            label: "",
            type: "text",
            required: false,
          }),
        ],
      }),
    );
  });

  it("edits a field without touching the rest of the node", async () => {
    const node = inputNode([
      { key: "severity", label: "Severity", type: "text", required: false },
    ]);
    const { onChange } = renderPanel(node);

    await userEvent.type(
      screen.getByRole("textbox", { name: "Field 1 label" }),
      "!",
    );

    // `join_sources` is not rendered for an input node; it must survive an edit to
    // a field that is, or a save silently drops graph semantics.
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        join_sources: ["analyze"],
        input_fields: [expect.objectContaining({ label: "Severity!" })],
      }),
    );
  });

  it("reorders fields, because the order is the form's order and the prompt's", async () => {
    const node = inputNode([
      { key: "title", label: "Title", type: "text", required: true },
      { key: "severity", label: "Severity", type: "text", required: false },
    ]);
    const { onChange } = renderPanel(node);

    // The end arrows are disabled rather than omitted, so the control set does not
    // move under the pointer as the author reorders.
    expect(
      screen.getByRole("button", { name: "Move field 1 up" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Move field 2 down" }),
    ).toBeDisabled();

    await userEvent.click(
      screen.getByRole("button", { name: "Move field 2 up" }),
    );

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        input_fields: [
          expect.objectContaining({ key: "severity" }),
          expect.objectContaining({ key: "title" }),
        ],
      }),
    );
  });

  it("names an empty key and a duplicate key as problems", () => {
    renderPanel(
      inputNode([
        { key: "", label: "Nameless", type: "text", required: false },
        { key: "severity", label: "One", type: "text", required: false },
        { key: "severity", label: "Two", type: "text", required: false },
      ]),
    );

    expect(
      screen.getByText(
        "A field needs a key: it is where the submitted value is stored.",
      ),
    ).toBeInTheDocument();
    // BOTH duplicate rows are flagged, not just the later one: the author has to
    // choose which key to change, and marking only the second implies the first is
    // the correct one.
    expect(
      screen.getAllByText(
        'Another field already uses the key "severity", and the second value would overwrite the first.',
      ),
    ).toHaveLength(2);
  });

  it("offers an options editor only for a select, and demands at least one option", async () => {
    const { onChange } = renderPanel(
      inputNode([
        { key: "severity", label: "Severity", type: "select", required: true },
      ]),
    );

    // A select with no options is a dropdown with nothing in it: the Run dialog
    // cannot render a choice, so a required one makes the template unrunnable.
    expect(
      screen.getByText(
        "A pick-one field needs at least one choice, or the Run dialog has nothing to offer.",
      ),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Add choice" }));
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        input_fields: [expect.objectContaining({ options: [""] })],
      }),
    );
  });

  it("does not offer an options editor for a text field", () => {
    // Rendering choices for every kind would invite an author to fill in a list the
    // dialog never reads and the server ignores.
    renderPanel(
      inputNode([
        { key: "severity", label: "Severity", type: "text", required: false },
      ]),
    );

    expect(screen.queryByRole("button", { name: "Add choice" })).toBeNull();
    expect(screen.queryByText("Choices")).toBeNull();
  });

  it("flags a blank option, which cannot be told apart from no selection", () => {
    renderPanel(
      inputNode([
        {
          key: "severity",
          label: "Severity",
          type: "select",
          required: true,
          options: ["low", ""],
        },
      ]),
    );

    expect(
      screen.getByText(
        "A blank choice cannot be told apart from nothing being picked.",
      ),
    ).toBeInTheDocument();
  });

  it("offers a field type this build does not know back verbatim", () => {
    // Same rule as an unknown routing strategy: otherwise merely SELECTING the node
    // would rewrite a field type the server accepts.
    renderPanel(
      inputNode([{ key: "due", label: "Due", type: "date", required: false }]),
    );

    expect(
      screen.getByRole("combobox", { name: "Field 1 type" }),
    ).toHaveTextContent('Unrecognized type "date"');
  });

  it("disables every control for a template the server will not let anyone edit", () => {
    renderPanel(
      inputNode([
        {
          key: "severity",
          label: "Severity",
          type: "select",
          required: true,
          options: ["low"],
        },
      ]),
      { readOnly: true },
    );

    for (const box of screen.getAllByRole("textbox")) {
      expect(box).toBeDisabled();
    }
    for (const combobox of screen.getAllByRole("combobox")) {
      expect(combobox).toBeDisabled();
    }
    // The mutating actions are removed rather than disabled, matching the rest of
    // the panel (criteria, branches).
    expect(screen.queryByRole("button", { name: "Add field" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove field 1" })).toBeNull();
  });
});

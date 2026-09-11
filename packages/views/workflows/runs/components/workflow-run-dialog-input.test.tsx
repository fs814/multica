// @vitest-environment jsdom

/**
 * The Run dialog's intake form: where a graph's declaration becomes a submitted
 * body.
 *
 * These tests exist because that seam is where the feature can fail SILENTLY, and
 * this feature's history is a list of green suites that were blind to their own
 * failure mode. So each fixture is chosen for what it lets fail:
 *
 *  - the declared-fields fixture declares `severity` and `repro_steps` BEYOND the
 *    title/description pair. A fixture whose only declared fields were title and
 *    description could not tell "the declaration is being read" from "the hardcoded
 *    pair is still being rendered", which already worked - and that is precisely
 *    the blind-fixture defect. `severity` is a select and `repro_steps` a textarea,
 *    so a dialog that rendered every field as a text input would fail too.
 *
 *  - the backward-compatibility fixture is the REAL pre-input-node Bug Fix graph
 *    (bug-fix.fixture.ts, entry `analyze`, an agent node), not a hand-written
 *    "graph with no input node". Every already-published template and every
 *    in-flight run's pinned version looks like that one, and a graph invented here
 *    could drift from it without anything noticing.
 *
 *  - the SUBMITTED BODY is asserted, not just the rendered controls. A dialog that
 *    renders a declared field beautifully and then drops it on submit would pass a
 *    render-only suite, and the value never reaching the run is the whole bug.
 *
 *  - the byte-identity test compares two bodies produced by the two different code
 *    paths from the same typed values. Asserting the declared path's body against a
 *    hand-written literal would prove only that the literal matches itself; running
 *    the fallback path for the expectation is what makes "a declaration of title +
 *    description changes nothing downstream" a test that can fail.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import {
  WorkflowDefinitionSchema,
  type WorkflowDefinition,
} from "@multica/core/workflows";
import enCommon from "../../../locales/en/common.json";
import enWorkflows from "../../../locales/en/workflows.json";
import enUi from "../../../locales/en/ui.json";
import enProjects from "../../../locales/en/projects.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../../navigation";
import { bugFixDefinition } from "../../graph/bug-fix.fixture";

const TEST_RESOURCES = {
  en: {
    common: enCommon,
    workflows: enWorkflows,
    ui: enUi,
    projects: enProjects,
  },
};

const runTemplateMock = vi.hoisted(() => vi.fn());
const saveInstanceMock = vi.hoisted(() => vi.fn());
const instanceFixtures = vi.hoisted(() => ({ rows: [] as Array<{id: string; templateId: string; name: string; input: Record<string,string>; projectId: string | null; revision: number; templateVersionId?: string | null}> }));
const pushMock = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
  useWorkspacePaths: () => ({
    workflowRuns: () => "/acme/workflow-runs",
    workflowRunDetail: (id: string) => `/acme/workflow-runs/${id}`,
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));
vi.mock("@multica/core/projects/queries", () => ({
  projectListOptions: (wsId: string) => ({
    queryKey: ["projects", wsId],
    queryFn: () => Promise.resolve([]),
  }),
}));
vi.mock("@multica/core/workflows", async () => {
  const actual = await vi.importActual<
    typeof import("@multica/core/workflows")
  >("@multica/core/workflows");
  return {
    ...actual,
    workflowInputInstanceListOptions: (wsId: string, templateId: string) => ({ queryKey: ["test-instances", wsId, templateId], queryFn: async () => instanceFixtures.rows }),
    useSaveWorkflowInputInstance: () => ({ mutateAsync: saveInstanceMock, isPending: false }),
    useDeleteWorkflowInputInstance: () => ({ mutateAsync: vi.fn(), isPending: false }),
    useRunWorkflowTemplate: () => ({
      mutateAsync: runTemplateMock,
      isPending: false,
    }),
  };
});
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
}));

import { WorkflowRunDialog } from "./workflow-run-dialog";

/**
 * A graph whose entry is an input node.
 *
 * Parsed through the REAL wire schema rather than hand-built as a
 * `WorkflowDefinition` literal, for the same reason bug-fix.fixture.ts is: the
 * server omits every defaulted key (no `next` on an End node, no `branches`
 * anywhere, no `options` on a text field), so a literal that filled them all in
 * would be a shape no server ever sends. It also means the schema's own
 * `input_fields` defaulting is exercised on the way in.
 */
function intakeDefinition(): WorkflowDefinition {
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
          {
            key: "title",
            label: "Headline",
            type: "text",
            required: true,
            placeholder: "One line naming the broken behaviour",
          },
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
            placeholder: "Numbered, shortest path first",
          },
          {
            key: "description",
            label: "Bug description",
            type: "textarea",
            required: true,
            placeholder: "What happened and what you expected",
          },
        ],
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

function renderDialog(
  props: Partial<React.ComponentProps<typeof WorkflowRunDialog>> = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const navigation: NavigationAdapter = {
    push: pushMock,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/workflows/wft-1",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
  const ui = (nextProps = props) => (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <WorkflowRunDialog
            templateId="wft-1"
            templateName="Bug Fix"
            runnable
            open
            onOpenChange={vi.fn()}
            {...nextProps}
          />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>
  );
  const view = render(ui());
  return { ...view, updateProps: (nextProps: typeof props) => view.rerender(ui({ ...props, ...nextProps })) };
}

/** Sets a text/textarea control by its accessible label. */
function type(label: string, value: string) {
  fireEvent.change(screen.getByRole("textbox", { name: label }), {
    target: { value },
  });
}

beforeEach(() => {
  instanceFixtures.rows = [];
  saveInstanceMock.mockReset();
  runTemplateMock.mockReset();
  runTemplateMock.mockResolvedValue({ id: "wfr-2", status: "running" });
  pushMock.mockReset();
});

describe("run dialog with a declared intake", () => {
  it("reuses an image selected on the node without asking the runner to select it again", async () => {
    const definition = WorkflowDefinitionSchema.parse({
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
    renderDialog({ definition });

    expect(screen.queryByRole("button", { name: "Choose image" })).toBeNull();
    fireEvent.change(screen.getByPlaceholderText("One line naming the work"), {
      target: { value: "Inspect screenshot" },
    });
    fireEvent.change(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
      { target: { value: "Use the image configured on the input node." } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() =>
      expect(runTemplateMock).toHaveBeenCalledWith({
        idempotency_key: expect.any(String),
        templateId: "wft-1",
        title: "Inspect screenshot",
        description: "Use the image configured on the input node.",
        project_id: null,
      }),
    );
  });

  it("renders the entry input node's declared fields, with the author's labels", () => {
    renderDialog({ definition: intakeDefinition() });

    // The author's own labels, not this build's: a declared field's label is
    // authored content, and showing "Title" over a field the author called
    // "Headline" would describe the graph inaccurately.
    expect(
      screen.getByRole("textbox", { name: /Headline/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("textbox", { name: /Steps to reproduce/ }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /^Title/ })).toBeNull();
    // The author's placeholder too, which is the only per-field guidance a
    // declaration can carry.
    expect(
      screen.getByPlaceholderText("Numbered, shortest path first"),
    ).toBeInTheDocument();
  });

  it("renders each declared kind as its own control, not everything as a text box", () => {
    renderDialog({ definition: intakeDefinition() });

    // A select is a combobox; rendering it as a text input would let a submitter
    // type an off-list value the server then refuses with a 422.
    expect(
      screen.getByRole("combobox", { name: "Severity" }),
    ).toBeInTheDocument();
    // textarea vs input: the difference is whether multi-line prose can be
    // entered at all, and `repro_steps` is declared as a textarea.
    expect(
      screen.getByRole("textbox", { name: /Steps to reproduce/ }).tagName,
    ).toBe("TEXTAREA");
    expect(screen.getByRole("textbox", { name: /Headline/ }).tagName).toBe(
      "INPUT",
    );
  });

  it("marks the required fields and leaves an optional one unmarked", () => {
    renderDialog({ definition: intakeDefinition() });

    // Required-ness is carried by `required`/`aria-required` on the control, not
    // folded into its accessible NAME: a "*" inside the label element would make
    // the field's name "Severity*", which is the identity a screen reader reads
    // back on every focus being corrupted by a property of the field.
    expect(screen.getByRole("textbox", { name: "Headline" })).toBeRequired();
    expect(screen.getByRole("combobox", { name: "Severity" })).toHaveAttribute(
      "aria-required",
      "true",
    );
    // `repro_steps` is declared optional, so marking it would be a claim the Run
    // button then contradicts by staying enabled.
    expect(
      screen.getByRole("textbox", { name: "Steps to reproduce" }),
    ).not.toBeRequired();
  });

  it("blocks submit with a per-field message rather than one toast", async () => {
    renderDialog({ definition: intakeDefinition() });
    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();

    // Everything except the required select. The button must stay refused, and the
    // dialog must name the field that is missing - a submitter told only "fill in
    // the required fields" has to re-read the whole form.
    type("Headline", "Claim endpoint 500s");
    type("Bug description", "Returns 500 for an empty queue.");
    const severity = screen.getByRole("combobox", { name: "Severity" });
    fireEvent.blur(severity);

    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
    expect(
      await screen.findByText("Severity is required to start this run."),
    ).toBeInTheDocument();
    // Only the offending field is called out.
    expect(
      screen.queryByText("Headline is required to start this run."),
    ).toBeNull();
    expect(runTemplateMock).not.toHaveBeenCalled();

    await userEvent.click(severity);
    await userEvent.click(await screen.findByRole("option", { name: "high" }));

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Run" })).toBeEnabled(),
    );
  });

  it("does not require a field the declaration marks optional", async () => {
    renderDialog({ definition: intakeDefinition() });

    type("Headline", "Claim endpoint 500s");
    type("Bug description", "Returns 500 for an empty queue.");
    await userEvent.click(screen.getByRole("combobox", { name: "Severity" }));
    await userEvent.click(await screen.findByRole("option", { name: "low" }));

    // `repro_steps` left blank on purpose: an optional field must not gate the run.
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Run" })).toBeEnabled(),
    );
  });

  it("submits the declared values under their declared keys", async () => {
    renderDialog({ definition: intakeDefinition() });

    type("Headline", "  Claim endpoint 500s  ");
    type("Bug description", "Returns 500 for an empty queue.");
    type("Steps to reproduce", "1. POST /api/daemon/claim");
    await userEvent.click(screen.getByRole("combobox", { name: "Severity" }));
    await userEvent.click(await screen.findByRole("option", { name: "high" }));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    // The keys are the DECLARED keys, flat - the shape of the run's input bag and
    // what `ParseRunInputFor` looks values up by. A dialog that rendered the
    // declaration and then dropped it here would pass every render assertion above.
    await waitFor(() =>
      expect(runTemplateMock).toHaveBeenCalledWith({
        idempotency_key: expect.any(String),
        templateId: "wft-1",
        title: "Claim endpoint 500s",
        description: "Returns 500 for an empty queue.",
        severity: "high",
        repro_steps: "1. POST /api/daemon/claim",
        project_id: null,
      }),
    );
  });

  it("omits an optional field left blank rather than sending an empty string", async () => {
    renderDialog({ definition: intakeDefinition() });

    type("Headline", "Claim endpoint 500s");
    type("Bug description", "Returns 500 for an empty queue.");
    await userEvent.click(screen.getByRole("combobox", { name: "Severity" }));
    await userEvent.click(await screen.findByRole("option", { name: "low" }));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(runTemplateMock).toHaveBeenCalled());
    const body = runTemplateMock.mock.calls[0]![0] as Record<string, unknown>;
    // `""` in the bag would be indistinguishable from an unanswered question, and
    // the server's reader skips it anyway - so it must not be written at all.
    expect("repro_steps" in body).toBe(false);
  });
});

describe("run dialog fallback for a template with no input node", () => {
  it("collects the freeform pair for the real pre-input-node Bug Fix graph", async () => {
    // Every already-published template looks like this, and a published version is
    // immutable - so this path can never stop working.
    renderDialog({ definition: bugFixDefinition() });

    expect(
      screen.getByPlaceholderText("One line naming the work"),
    ).toBeInTheDocument();
    expect(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
    ).toBeInTheDocument();
    // The hint under the description belongs to the built-in control only.
    expect(
      screen.getByText(
        "Every agent step gets its own instruction plus this text. Without it the first agent has nothing concrete to work on.",
      ),
    ).toBeInTheDocument();
  });

  it("falls back when the dialog is given no definition at all", () => {
    // A caller with no graph to hand (and the degraded-parse case, which lands
    // here as an empty definition) must still be able to start a run.
    renderDialog();
    expect(
      screen.getByPlaceholderText("One line naming the work"),
    ).toBeInTheDocument();
  });

  it("falls back for a graph whose entry input node is misplaced", () => {
    // An input node that is not the entry is a graph the server's validator
    // rejects, but an unpublished draft can still reach a client. "No intake
    // declaration" is the honest reading; showing the mid-graph node's form would
    // collect values the engine never checks.
    const misplaced = WorkflowDefinitionSchema.parse({
      schema_version: 1,
      entry_node: "analyze",
      nodes: [
        {
          key: "analyze",
          type: "agent",
          next: ["intake"],
          routing: { strategy: "capability", capability: "bug_analysis" },
        },
        {
          key: "intake",
          type: "input",
          next: ["end"],
          input_fields: [
            {
              key: "severity",
              label: "Severity",
              type: "text",
              required: true,
            },
          ],
        },
        { key: "end", type: "end" },
      ],
    });
    renderDialog({ definition: misplaced });

    expect(
      screen.getByPlaceholderText("One line naming the work"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: /Severity/ })).toBeNull();
  });
});

describe("declared title + description must not change the submitted body", () => {
  it("produces the same body as the fallback form for the same values", async () => {
    // The seeded bug_fix intake declares exactly title + description. If declaring
    // them changed the body, the first agent's prompt would change shape for a
    // template that only became self-documenting - so the two paths are compared
    // directly rather than against a literal that could match only itself.
    const declaredPair = WorkflowDefinitionSchema.parse({
      schema_version: 1,
      entry_node: "intake",
      nodes: [
        {
          key: "intake",
          type: "input",
          next: ["end"],
          input_fields: [
            { key: "title", label: "Title", type: "text", required: true },
            {
              key: "description",
              label: "Bug description",
              type: "textarea",
              required: true,
            },
          ],
        },
        { key: "end", type: "end" },
      ],
    });

    renderDialog({ definition: declaredPair });
    type("Title", "Claim endpoint 500s");
    type("Bug description", "Returns 500 for an empty queue.");
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(runTemplateMock).toHaveBeenCalled());
    const declaredBody = runTemplateMock.mock.calls[0]![0];

    runTemplateMock.mockClear();
    // A real unmount, not just clearing the DOM: the dialog holds the typed values
    // in component state, and a second render alongside the first would leave two
    // "Run" buttons on screen for `getByRole` to choose between.
    cleanup();

    renderDialog({ definition: bugFixDefinition() });
    fireEvent.change(screen.getByPlaceholderText("One line naming the work"), {
      target: { value: "Claim endpoint 500s" },
    });
    fireEvent.change(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
      { target: { value: "Returns 500 for an empty queue." } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(runTemplateMock).toHaveBeenCalled());
    const fallbackBody = runTemplateMock.mock.calls[0]![0];

    // Byte-identical, key order included: the run's idempotency key is derived
    // from the title and description, and the input bag is stored as sent.
    expect({ ...declaredBody, idempotency_key: undefined }).toEqual({ ...fallbackBody, idempotency_key: undefined });
  });

  it("still requires title and description when the declaration marks them optional", async () => {
    // The endpoint answers 400 for an empty title or description on EVERY run, so
    // honouring `required: false` here would enable a body the server refuses and
    // hand the submitter an error they cannot act on.
    const optionalPair = WorkflowDefinitionSchema.parse({
      schema_version: 1,
      entry_node: "intake",
      nodes: [
        {
          key: "intake",
          type: "input",
          next: ["end"],
          input_fields: [
            { key: "title", label: "Title", type: "text", required: false },
            {
              key: "description",
              label: "Description",
              type: "textarea",
              required: false,
            },
          ],
        },
        { key: "end", type: "end" },
      ],
    });
    renderDialog({ definition: optionalPair });

    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
    type("Title", "Claim endpoint 500s");
    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
    type("Description", "Returns 500 for an empty queue.");
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Run" })).toBeEnabled(),
    );
  });

  it("supplies the built-in description control when a declaration omits it", async () => {
    // A declaration that asks only for a title still has to produce a description,
    // or the run cannot start at all.
    const titleOnly = WorkflowDefinitionSchema.parse({
      schema_version: 1,
      entry_node: "intake",
      nodes: [
        {
          key: "intake",
          type: "input",
          next: ["end"],
          input_fields: [
            { key: "title", label: "Headline", type: "text", required: true },
          ],
        },
        { key: "end", type: "end" },
      ],
    });
    renderDialog({ definition: titleOnly });

    expect(
      screen.getByRole("textbox", { name: /Headline/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByPlaceholderText(
        "What happened, how to reproduce it, and what done looks like",
      ),
    ).toBeInTheDocument();

    type("Headline", "Claim endpoint 500s");
    type("Description", "Returns 500 for an empty queue.");
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() =>
      expect(runTemplateMock).toHaveBeenCalledWith({
        idempotency_key: expect.any(String),
        templateId: "wft-1",
        title: "Claim endpoint 500s",
        description: "Returns 500 for an empty queue.",
        project_id: null,
      }),
    );
  });
});

 describe("saved workflow input instances", () => {
  it("keeps instance creation out of the run dialog", () => {
    renderDialog();
    expect(screen.queryByRole("textbox",{name:"Instance name"})).not.toBeInTheDocument();
    expect(screen.queryByRole("button",{name:"Save as instance"})).not.toBeInTheDocument();
  });

  it("uses a new run key when the same inputs are run again", async () => {
    const runOnce = async () => {
      renderDialog();
      fireEvent.change(screen.getByPlaceholderText("One line naming the work"), { target: { value: "Repeat" } });
      fireEvent.change(screen.getByPlaceholderText("What happened, how to reproduce it, and what done looks like"), { target: { value: "Same input" } });
      fireEvent.click(screen.getByRole("button", { name: "Run" }));
      await waitFor(() => expect(pushMock).toHaveBeenCalled());
      const key = runTemplateMock.mock.lastCall![0].idempotency_key;
      cleanup();
      pushMock.mockClear();
      return key;
    };
    expect(await runOnce()).not.toBe(await runOnce());
  });
 });
describe("input node content as run defaults", () => {
  function authoredInput() {
    const definition = intakeDefinition();
    const entry = definition.nodes[0]!;
    entry.name = "List system version";
    entry.instruction = "Machine: MacBook\nReport the OS version and architecture.";
    entry.input_fields = [];
    return definition;
  }

  it("submits authored node content without typing it again", async () => {
    renderDialog({ definition: authoredInput() });
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("List system version");
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Machine: MacBook\nReport the OS version and architecture.");
    expect(screen.getByRole("button", { name: "Run" })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(runTemplateMock).toHaveBeenCalledWith(expect.objectContaining({
      title: "List system version",
      description: "Machine: MacBook\nReport the OS version and architecture.",
    })));
  });

  it("uses current editor content and lets the user clear or override it", async () => {
    renderDialog({ definition: authoredInput(), inputDefaults: { title: "Current title", description: "Current node content" } });
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Current node content");
    type("Description", "");
    expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("");
    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
    type("Description", "Run-specific content");
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(runTemplateMock).toHaveBeenCalledWith(expect.objectContaining({
      title: "Current title", description: "Run-specific content",
    })));
  });

  it("uses the template name when only key information was entered", () => {
    const definition = authoredInput();
    definition.nodes[0]!.name = "";
    renderDialog({ definition });
    expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Bug Fix");
    expect(screen.getByRole("button", { name: "Run" })).toBeEnabled();
  });

  it("still collects required custom fields from the published schema", () => {
    renderDialog({ definition: intakeDefinition(), inputDefaults: { title: "Ready title", description: "Ready description" } });
    expect(screen.getByRole("textbox", { name: "Headline" })).toHaveValue("Ready title");
    expect(screen.getByRole("textbox", { name: "Bug description" })).toHaveValue("Ready description");
    expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Severity" })).toBeInTheDocument();
  });


});
it("starts a reopened Run dialog from the latest node content without carrying edits to another template", () => {
  const view = renderDialog({ inputDefaults: { title: "Original", description: "Node content" } });
  type("Description", "Temporary run override");
  view.updateProps({ open: false });
  view.updateProps({ open: true, inputDefaults: { title: "Updated", description: "Updated node content" } });
  expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Updated");
  expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Updated node content");
  type("Description", "Another temporary override");
  view.updateProps({ templateId: "another-template", inputDefaults: { title: "Other workflow", description: "Other content" } });
  expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Other content");
});

it("keeps edits and requires review if the publication changes while the form is open", () => {
  const view = renderDialog({ templateVersionId: "v1", inputDefaults: { title: "Task", description: "Default" } });
  type("Description", "Unsaved input");
  view.updateProps({ templateVersionId: "v2" });
  expect(screen.getByRole("textbox", { name: "Description" })).toHaveValue("Unsaved input");
  expect(screen.getByRole("button", { name: "Run" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Use current fields" }));
  expect(screen.getByRole("button", { name: "Run" })).toBeEnabled();
});

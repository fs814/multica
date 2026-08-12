// @vitest-environment jsdom

import type { ComponentProps, ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enUi from "../../locales/en/ui.json";
import enWorkflows from "../../locales/en/workflows.json";

const TEST_RESOURCES = {
  en: { common: enCommon, ui: enUi, workflows: enWorkflows },
};

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  push: vi.fn(),
  refetch: vi.fn(),
  toastSuccess: vi.fn(),
  runtimes: [] as Array<Record<string, unknown>>,
  agents: [] as Array<Record<string, unknown>>,
  createBuilderSession: vi.fn(),
  sendChatMessage: vi.fn(),
  listChatMessages: vi.fn(),
  getPendingChatTask: vi.fn(),
  validateWorkflowDefinition: vi.fn(),
  deleteChatSession: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  queryOptions: <T,>(options: T) => options,
  useQuery: (options: { queryKey?: unknown[] }) => {
    const key = options.queryKey ?? [];
    const data =
      key[0] === "runtimes"
        ? mocks.runtimes
        : key.includes("agents")
          ? mocks.agents
          : [];
    return {
      data,
      isLoading: false,
      error: null,
      refetch: mocks.refetch,
    };
  },
}));

vi.mock("@multica/core/workflows", async () => {
  const actual =
    await vi.importActual<typeof import("@multica/core/workflows")>(
      "@multica/core/workflows",
    );
  return {
    ...actual,
    workflowTemplateListOptions: () => ({ queryKey: ["workflow-list"] }),
    useCreateWorkflowTemplate: () => ({
      mutateAsync: mocks.create,
      isPending: false,
    }),
  };
});

vi.mock("@multica/core/api", () => ({
  api: {
    createWorkflowBuilderSession: mocks.createBuilderSession,
    sendChatMessage: mocks.sendChatMessage,
    listChatMessages: mocks.listChatMessages,
    getPendingChatTask: mocks.getPendingChatTask,
    validateWorkflowDefinition: mocks.validateWorkflowDefinition,
    deleteChatSession: mocks.deleteChatSession,
  },
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (
    selector: (state: { user: { id: string } | null }) => unknown,
  ) => selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    workflowDetail: (id: string) => `/acme/workflows/${id}`,
  }),
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => ({ push: mocks.push }),
  useRowLink: () => () => ({}),
}));

vi.mock("../../agents/components/runtime-picker", () => ({
  isRuntimeUsableForUser: () => true,
}));

vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess },
}));

vi.mock("../../layout/collection-page", () => ({
  CollectionPageHeader: ({
    title,
    actions,
  }: {
    title: ReactNode;
    actions?: ReactNode;
  }) => (
    <header>
      <h1>{title}</h1>
      {actions}
    </header>
  ),
  CollectionPageHeaderAction: ({
    label,
    onClick,
  }: {
    label: string;
    onClick: () => void;
  }) => (
    <button type="button" onClick={onClick}>
      {label}
    </button>
  ),
  CollectionPageState: ({
    title,
    description,
    actions,
  }: {
    title: ReactNode;
    description?: ReactNode;
    actions?: ReactNode;
  }) => (
    <section>
      <h2>{title}</h2>
      <p>{description}</p>
      {actions}
    </section>
  ),
}));

vi.mock("@multica/ui/components/ui/dialog", () => ({
  Dialog: ({ open, children }: { open: boolean; children: ReactNode }) =>
    open ? <div role="dialog">{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogDescription: ({ children }: { children: ReactNode }) => <p>{children}</p>,
  DialogFooter: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
}));

vi.mock("@multica/ui/components/ui/button", () => ({
  Button: ({
    variant: _variant,
    size: _size,
    ...props
  }: ComponentProps<"button"> & { variant?: string; size?: string }) => (
    <button {...props} />
  ),
}));

vi.mock("@multica/ui/components/ui/input", () => ({
  Input: (props: ComponentProps<"input">) => <input {...props} />,
}));

vi.mock("@multica/ui/components/ui/label", () => ({
  Label: (props: ComponentProps<"label">) => <label {...props} />,
}));

vi.mock("@multica/ui/components/ui/textarea", () => ({
  Textarea: (props: ComponentProps<"textarea">) => <textarea {...props} />,
}));

import { WorkflowsPage } from "./workflows-page";

function renderPage() {
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <WorkflowsPage />
    </I18nProvider>,
  );
}

function openDialog() {
  fireEvent.click(screen.getByRole("button", { name: "New workflow" }));
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.runtimes = [];
  mocks.agents = [];
  mocks.create.mockResolvedValue({ id: "new-id", key: "release_review" });
  mocks.createBuilderSession.mockResolvedValue({
    session_id: "builder-session",
    builder_agent_id: "builder-agent",
    runtime_id: "runtime-codex",
  });
  mocks.sendChatMessage.mockResolvedValue({
    message_id: "message-1",
    task_id: "task-1",
  });
  mocks.getPendingChatTask.mockResolvedValue({ task_id: "task-1" });
  mocks.validateWorkflowDefinition.mockResolvedValue({
    valid: true,
    messages: [],
  });
  mocks.deleteChatSession.mockResolvedValue(undefined);
});

describe("WorkflowsPage creation", () => {
  it("opens the create dialog from the page header", () => {
    renderPage();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    openDialog();

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Create workflow" }),
    ).toBeInTheDocument();
  });

  it("creates an Input to End draft and opens its editor", async () => {
    renderPage();
    openDialog();

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: " Release Review " },
    });
    expect(screen.getByLabelText("Key")).toHaveValue("release_review");
    fireEvent.change(screen.getByLabelText("Description"), {
      target: { value: " Validate the release candidate. " },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Create workflow" }),
    );

    await waitFor(() =>
      expect(mocks.create).toHaveBeenCalledWith({
        key: "release_review",
        name: "Release Review",
        description: "Validate the release candidate.",
        definition: {
          schema_version: 1,
          entry_node: "input",
          nodes: [
            {
              key: "input",
              type: "input",
              name: "Input",
              next: ["end"],
              input_fields: [],
            },
            {
              key: "end",
              type: "end",
              name: "End",
              next: [],
            },
          ],
        },
      }),
    );
    await waitFor(() =>
      expect(mocks.push).toHaveBeenCalledWith("/acme/workflows/new-id"),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("uses an online Codex runtime to generate, preview, and create a workflow", async () => {
    mocks.runtimes = [
      {
        id: "runtime-codex",
        provider: "codex",
        status: "online",
        owner_id: "user-1",
        visibility: "private",
        name: "Codex on workstation",
        custom_name: "",
      },
    ];
    mocks.agents = [
      {
        id: "agent-engineer",
        runtime_id: "runtime-codex",
        name: "Engineer",
        description: "Implements changes",
        archived_at: null,
      },
    ];
    const generatedDefinition = {
      schema_version: 1,
      entry_node: "input",
      nodes: [
        {
          key: "input",
          type: "input",
          name: "Request",
          next: ["implement"],
          input_fields: [],
        },
        {
          key: "implement",
          type: "agent",
          name: "Implement",
          instruction: "Implement the requested change.",
          next: ["end"],
          routing: {
            strategy: "explicit",
            agent_id: "agent-engineer",
          },
        },
        { key: "end", type: "end", name: "End", next: [] },
      ],
    };
    mocks.listChatMessages.mockResolvedValue([
      {
        id: "assistant-message",
        chat_session_id: "builder-session",
        role: "assistant",
        content: `<workflow_draft>${JSON.stringify({
          name: "Release Flow",
          key: "release_flow",
          description: "Implements and reviews a release.",
          definition: generatedDefinition,
        })}</workflow_draft>`,
        created_at: "2026-08-06T00:00:00Z",
      },
    ]);

    renderPage();
    openDialog();
    fireEvent.click(
      screen.getByRole("button", { name: "Generate with AI" }),
    );
    fireEvent.change(screen.getByLabelText("Describe the workflow"), {
      target: { value: "Implement and review each release." },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Generate workflow" }),
    );

    expect(await screen.findByDisplayValue("Release Flow")).toBeInTheDocument();
    expect(mocks.createBuilderSession).toHaveBeenCalledWith({
      runtime_id: "runtime-codex",
    });
    expect(mocks.sendChatMessage).toHaveBeenCalledWith(
      "builder-session",
      expect.stringContaining("agent-engineer"),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Create this workflow" }),
    );

    await waitFor(() => expect(mocks.create).toHaveBeenCalled());
    const request = mocks.create.mock.calls[0]?.[0];
    expect(request).toMatchObject({
      key: "release_flow",
      name: "Release Flow",
      description: "Implements and reviews a release.",
    });
    expect(
      request.definition.nodes.map((node: { key: string }) => node.key),
    ).toEqual(["input", "implement", "end"]);
    expect(mocks.push).toHaveBeenCalledWith("/acme/workflows/new-id");
  });

  it("asks Codex once to repair a completed response with no readable draft", async () => {
    mocks.runtimes = [
      {
        id: "runtime-codex",
        provider: "codex",
        status: "online",
        owner_id: "user-1",
        visibility: "private",
        name: "Codex on workstation",
        custom_name: "",
      },
    ];
    mocks.agents = [
      {
        id: "agent-engineer",
        runtime_id: "runtime-codex",
        name: "Engineer",
        description: "Implements changes",
        archived_at: null,
      },
    ];
    const repairedDraft = {
      name: "Requirement Delivery",
      key: "requirement_delivery",
      description: "Analyzes, implements, and verifies a requirement.",
      definition: {
        schema_version: 1,
        entry_node: "input",
        nodes: [
          { key: "input", type: "input", next: ["work"], input_fields: [] },
          {
            key: "work",
            type: "agent",
            instruction: "Implement and verify the requirement.",
            next: ["end"],
            routing: { strategy: "explicit", agent_id: "agent-engineer" },
          },
          { key: "end", type: "end", next: [] },
        ],
      },
    };
    mocks.listChatMessages
      .mockResolvedValueOnce([
        {
          id: "assistant-unreadable",
          chat_session_id: "builder-session",
          role: "assistant",
          content: "I designed the requested workflow.",
          created_at: "2026-08-06T00:00:00Z",
        },
      ])
      .mockResolvedValue([
        {
          id: "assistant-repaired",
          chat_session_id: "builder-session",
          role: "assistant",
          content: `<workflow_draft>${JSON.stringify(repairedDraft)}</workflow_draft>`,
          created_at: "2026-08-06T00:00:01Z",
        },
      ]);
    mocks.getPendingChatTask.mockResolvedValue({ task_id: null });

    renderPage();
    openDialog();
    fireEvent.click(screen.getByRole("button", { name: "Generate with AI" }));
    fireEvent.change(screen.getByLabelText("Describe the workflow"), {
      target: { value: "Analyze, implement, and verify a requirement." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Generate workflow" }));

    expect(
      await screen.findByDisplayValue("Requirement Delivery"),
    ).toBeInTheDocument();
    expect(mocks.sendChatMessage).toHaveBeenCalledTimes(2);
    expect(mocks.sendChatMessage.mock.calls[1]?.[1]).toContain(
      "MULTICA_WORKFLOW_BUILDER_FORMAT_REPAIR_V1",
    );
  });

  it("keeps the dialog open and shows a create failure", async () => {
    mocks.create.mockRejectedValue(
      new Error("a workflow template with that key already exists"),
    );
    renderPage();
    openDialog();

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Release Review" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Create workflow" }),
    );

    expect(
      await screen.findByRole("alert"),
    ).toHaveTextContent("a workflow template with that key already exists");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(mocks.push).not.toHaveBeenCalled();
  });
});

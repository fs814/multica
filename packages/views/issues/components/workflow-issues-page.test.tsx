// @vitest-environment jsdom

import React from "react";
import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkflowIssuesPage } from "./workflow-issues-page";

const mocks = vi.hoisted(() => ({
  navigation: {
    pathname: "/acme/workflow-issues",
    searchParams: new URLSearchParams(),
    replace: vi.fn(),
  },
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({
    data: [{ id: "template-1", name: "Template One" }],
    isLoading: false,
    error: null,
    refetch: vi.fn(),
  }),
}));

vi.mock("@multica/core/workflows", () => ({
  workflowTemplateListOptions: () => ({ queryKey: ["workflow-templates"] }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    workflowIssues: () => "/acme/workflow-issues",
  }),
}));

vi.mock("@multica/core/issues/stores/view-store-context", () => ({
  useViewStore: (selector: (state: unknown) => unknown) =>
    selector({ dateFilter: null, setDateFilter: vi.fn() }),
}));

vi.mock("../../navigation", () => ({
  useNavigation: () => mocks.navigation,
}));

vi.mock("../../i18n", () => ({
  useT: () => ({ t: vi.fn() }),
}));

vi.mock("../../layout/page-header", () => ({
  PageHeader: ({
    children,
  }: {
    children: React.ReactNode;
    className?: string;
  }) => <div>{children}</div>,
}));

vi.mock("../surface/issue-surface", () => ({
  IssueSurface: () => <div />,
}));

vi.mock("./issues-header", () => ({
  IssuesHeader: () => <div />,
}));

vi.mock("@multica/ui/components/ui/select", () => ({
  Select: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SelectContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SelectItem: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SelectTrigger: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SelectValue: () => null,
}));

describe("WorkflowIssuesPage", () => {
  beforeEach(() => {
    mocks.navigation.pathname = "/acme/workflow-issues";
    mocks.navigation.searchParams = new URLSearchParams();
    mocks.navigation.replace.mockClear();
  });

  it("adds the default template on the workflow issues route", () => {
    render(<WorkflowIssuesPage />);

    expect(mocks.navigation.replace).toHaveBeenCalledWith(
      "/acme/workflow-issues?template=template-1",
    );
  });

  it("does not replace while the still-mounted page observes another route", () => {
    mocks.navigation.pathname = "/acme/issues";

    render(<WorkflowIssuesPage />);

    expect(mocks.navigation.replace).not.toHaveBeenCalled();
  });
});

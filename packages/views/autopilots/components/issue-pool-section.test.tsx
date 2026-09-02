import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import type { IssuePoolCycle } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { IssuePoolCycleReview } from "./issue-pool-section";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const cycle: IssuePoolCycle = {
  id: "cycle-1", policy_id: "policy-1", autopilot_id: "autopilot-1",
  workspace_id: "workspace-1", project_id: null, idempotency_key: "autopilot:run-1",
  status: "awaiting_review", scanned_count: 8, eligible_count: 3, claimed_count: 2,
  approved_count: 0, rejected_count: 0, completed_count: 0, blocked_count: 0,
  failed_count: 0, deferred_count: 0, created_at: "2026-09-02T00:00:00Z",
  updated_at: "2026-09-02T00:00:00Z", reviewed_at: null, completed_at: null,
  autopilot_run_id: "autopilot-run-1", workflow_template_id: "template-1",
  workflow_template_version_id: "version-1",
  items: [
    {
      id: "item-1", issue_id: "issue-1", status: "claimed", score: 314,
      score_breakdown: { total: 314 },
      selection_reasons: ["status:backlog", "inactive:14d", "priority:high"],
      issue_snapshot: { title: "Repair checkout" }, claim_token: "claim-1",
      reviewer_id: null, review_reason: null, claimed_at: "2026-09-02T00:00:00Z",
      reviewed_at: null, workflow_run_id: null, dispatch_attempts: 0,
      failure_code: null, dispatched_at: null, waiting_acceptance_at: null, completed_at: null,
    },
    {
      id: "item-2", issue_id: "issue-2", status: "claimed", score: 103,
      score_breakdown: { total: 103 }, selection_reasons: ["status:todo", "priority:low"],
      issue_snapshot: { title: "Refresh docs" }, claim_token: "claim-2",
      reviewer_id: null, review_reason: null, claimed_at: "2026-09-02T00:00:00Z",
      reviewed_at: null, workflow_run_id: null, dispatch_attempts: 0,
      failure_code: null, dispatched_at: null, waiting_acceptance_at: null, completed_at: null,
    },
  ],
};

describe("IssuePoolCycleReview browser interaction", () => {
  it("links original issues and submits selected candidates as one approval", async () => {
    const onReview = vi.fn().mockResolvedValue(undefined);
    renderWithI18n(
      <IssuePoolCycleReview
        cycle={cycle}
        canWrite
        issueHref={(id) => `/acme/issues/${id}`}
        workflowHref={(id) => `/acme/workflow-runs/${id}`}
        onReview={onReview}
      />,
    );
    expect(screen.getByRole("link", { name: "Repair checkout" })).toHaveAttribute(
      "href", "/acme/issues/issue-1",
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Approve (2)" })).toBeEnabled());
    fireEvent.click(screen.getByRole("checkbox", { name: "Select Refresh docs" }));
    fireEvent.click(screen.getByRole("button", { name: "Approve (1)" }));
    await waitFor(() => expect(onReview).toHaveBeenCalledWith([
      { item_id: "item-1", decision: "approve", reason: undefined },
    ]));
  });

  it("exposes blocked remediation and the canonical workflow-run link", () => {
    const blocked: IssuePoolCycle = {
      ...cycle,
      status: "blocked",
      items: [{
        ...cycle.items[0]!,
        status: "blocked",
        workflow_run_id: "workflow-run-1",
        failure_code: "workflow_blocked",
        failure_detail: { message: "Runtime unavailable" },
      }],
    };
    renderWithI18n(
      <IssuePoolCycleReview
        cycle={blocked}
        canWrite
        issueHref={(id) => `/acme/issues/${id}`}
        workflowHref={(id) => `/acme/workflow-runs/${id}`}
        onReview={vi.fn()}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Runtime unavailable");
    expect(screen.getByRole("link", { name: "Open workflow run" })).toHaveAttribute(
      "href", "/acme/workflow-runs/workflow-run-1",
    );
  });
});

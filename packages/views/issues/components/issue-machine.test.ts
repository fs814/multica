// @vitest-environment node
import { describe, expect, it } from "vitest";
import { resolveIssueMachine } from "./issue-machine";

const local = { id: "runtime-local", name: "Codex (this machine)", custom_name: "Local" };
const remote = { id: "runtime-remote", name: "Codex (worker)", custom_name: "Worker" };

describe("issue execution machine", () => {
  it("resolves a squad assignment through its leader's machine before the first run", () => {
    expect(resolveIssueMachine(
      { assignee_type: "squad", assignee_id: "squad-5090" },
      [{ id: "leader-5090", runtime_id: remote.id }], [local, remote], [],
      [{ id: "squad-5090", leader_id: "leader-5090" }],
    )).toEqual({ source: "assigned", name: "Worker" });
  });

  it("preserves the actual execution machine when a squad leader is rebound", () => {
    expect(resolveIssueMachine(
      { assignee_type: "squad", assignee_id: "squad-5090" },
      [{ id: "leader-5090", runtime_id: local.id }], [local, remote],
      [{ runtime_id: remote.id, created_at: "2026-01-02T00:00:00Z" }],
      [{ id: "squad-5090", leader_id: "leader-5090" }],
    )).toEqual({ source: "last_run", name: "Worker" });
  });

  it("does not guess a target from another agent when a squad or leader is unavailable", () => {
    const issue = { assignee_type: "squad" as const, assignee_id: "squad-5090" };
    const agents = [{ id: "unrelated-agent", runtime_id: local.id }];
    expect(resolveIssueMachine(issue, agents, [local], [], [])).toEqual({ source: "unavailable" });
    expect(resolveIssueMachine(issue, agents, [local], [], [
      { id: "squad-5090", leader_id: "missing-leader" },
    ])).toEqual({ source: "unavailable" });
  });

  it("shows the assigned runtime before the first run", () => {
    expect(resolveIssueMachine(
      { assignee_type: "agent", assignee_id: "agent-1" },
      [{ id: "agent-1", runtime_id: local.id }], [local, remote], [],
    )).toEqual({ source: "assigned", name: "Local" });
  });

  it("shows the latest execution machine even if the agent is rebound", () => {
    expect(resolveIssueMachine(
      { assignee_type: "agent", assignee_id: "agent-1" },
      [{ id: "agent-1", runtime_id: local.id }], [local, remote],
      [{ runtime_id: local.id, created_at: "2026-01-01T00:00:00Z" },
        { runtime_id: remote.id, created_at: "2026-01-02T00:00:00Z" }],
    )).toEqual({ source: "last_run", name: "Worker" });
  });

  it("does not invent a local machine for a Center issue with no target", () => {
    expect(resolveIssueMachine(
      { assignee_type: "member", assignee_id: "member-1" }, [], [local], [],
    )).toEqual({ source: "unselected" });
  });
});

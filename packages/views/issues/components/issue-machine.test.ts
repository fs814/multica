import { describe, expect, it } from "vitest";
import { resolveIssueMachine } from "./issue-machine";

const local = { id: "runtime-local", name: "Codex (this machine)", custom_name: "Local" };
const remote = { id: "runtime-remote", name: "Codex (worker)", custom_name: "Worker" };

describe("issue execution machine", () => {
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

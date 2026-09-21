import { render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { SquadMachines } from "./squad-machines";
import en from "../../locales/en/runtimes.json";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/workspace/queries", () => ({ agentListOptions: () => ({ queryKey: ["agents"] }), workspaceKeys: { squads: () => ["squads"] } }));
vi.mock("@multica/core/runtimes", async importOriginal => ({ ...(await importOriginal<object>()), runtimeListOptions: () => ({ queryKey: ["runtimes"] }) }));
vi.mock("../../runtimes/components/execution-location", () => ({ ExecutionLocation: ({ agentId }: { agentId: string }) => <span>{agentId}</span> }));
vi.mock("../../i18n", () => ({ useT: () => ({ t: (select: (value: typeof en) => string) => select(en) }) }));
vi.mock("@tanstack/react-query", () => ({ useQuery: ({ queryKey }: { queryKey: string[] }) => ({ data: queryKey[0] === "agents" ? [{ id: "leader", runtime_id: "one" }, { id: "member", runtime_id: "two" }] : queryKey[0] === "runtimes" ? [
  { id: "one", daemon_id: "aaaaaaaa-long", custom_name: "Mac", name: "Codex (Mac)", provider: "codex", runtime_mode: "local", status: "online" },
  { id: "two", daemon_id: "bbbbbbbb-long", custom_name: "Windows", name: "Codex (Windows)", provider: "codex", runtime_mode: "local", status: "offline" },
] : [{ member_type: "agent", member_id: "leader" }, { member_type: "agent", member_id: "member" }, { member_type: "member", member_id: "human" }] }) }));

// Name/identity/privacy edge cases are covered by the pure projection suite.
it("wires the full member list into the machine summary without double-counting the leader or counting humans", () => {
  render(<SquadMachines squadId="squad" leaderId="leader" />);
  expect(screen.getByText(/Mac · aaaaaaaa × 1; Windows · bbbbbbbb × 1/)).toBeInTheDocument();
  expect(screen.getByText(en.execution.binding_note)).toBeInTheDocument();
});

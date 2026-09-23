import type { Agent } from "@multica/core/types";
import { render, screen, cleanup } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { ExecutionLocation } from "./execution-location";
import en from "../../locales/en/runtimes.json";

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws" }));
vi.mock("@multica/core/workspace/queries", () => ({ agentListOptions: () => ({ queryKey: ["agents"] }) }));
vi.mock("@multica/core/runtimes", async importOriginal => ({ ...(await importOriginal<object>()), runtimeListOptions: () => ({ queryKey: ["runtimes"] }) }));
vi.mock("../../i18n", () => ({ useT: () => ({ t: (select: (value: typeof en) => string) => select(en) }) }));
vi.mock("@tanstack/react-query", () => ({ useQuery: ({ queryKey }: { queryKey: string[] }) => ({ data: queryKey[0] === "agents" ? [{ id: "agent", runtime_id: "runtime", runtime_config: { knot: { client_uuid: "remote" } } }] : [{ id: "runtime", daemon_id: "12345678-long", name: "Knot (Mac)", custom_name: "Office", provider: "knot-http", runtime_mode: "local", status: "online", device_info: "macOS arm64" }] }) }));
afterEach(cleanup);
it("renders a Multica node separately from the unverified Knot requested target", () => {
  render(<ExecutionLocation agentId="agent" />);
  expect(screen.getByText(/Office · 12345678/)).toBeInTheDocument();
  expect(screen.getByText(/platform selection \(unverified\)/)).toHaveAttribute("title", en.execution.knot_unverified);
});
it("renders human members as not applicable", () => {
  render(<ExecutionLocation human />);
  expect(screen.getByText("Machine: not applicable")).toBeInTheDocument();
});

it("uses authorized detail data even when the agent is absent from the workspace list", () => {
  const detailAgent = { id: "detail-only", runtime_id: "runtime", runtime_config: {} } as Agent;
  render(<ExecutionLocation agent={detailAgent} />);
  expect(screen.getByText(/Office · 12345678/)).toBeInTheDocument();
  expect(screen.getByText(/local client \(strict\)/)).toBeInTheDocument();
});

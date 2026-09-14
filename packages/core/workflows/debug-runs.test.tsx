// @vitest-environment jsdom
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { expect, it, vi } from "vitest";
import { api } from "../api";
import { useStartWorkflowDebugRun, workflowDebugKeys } from "./debug-runs";
import type {
  StartWorkflowDebugRequest,
  WorkflowDebugRun,
} from "./debug-schemas";
vi.mock("../api", () => ({ api: { startWorkflowDebugRun: vi.fn() } }));
it("binds the request and late response cache to the originating workspace", async () => {
  let resolve!: (value: WorkflowDebugRun) => void;
  vi.mocked(api.startWorkflowDebugRun).mockReturnValue(
    new Promise((r) => {
      resolve = r;
    }),
  );
  const qc = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  });
  const spy = vi.spyOn(qc, "invalidateQueries");
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
  const hook = renderHook(({ wsId }) => useStartWorkflowDebugRun(wsId), {
    wrapper,
    initialProps: { wsId: "ws-a" },
  });
  const body = {} as StartWorkflowDebugRequest;
  act(() => hook.result.current.mutate({ id: "template", body }));
  await waitFor(() =>
    expect(api.startWorkflowDebugRun).toHaveBeenCalledWith(
      "template",
      body,
      "ws-a",
    ),
  );
  hook.rerender({ wsId: "ws-b" });
  const value = {
    run: { id: "run-a", workspace_id: "ws-a" },
  } as WorkflowDebugRun;
  await act(async () => resolve(value));
  await waitFor(() =>
    expect(qc.getQueryData(workflowDebugKeys.detail("ws-a", "run-a"))).toBe(
      value,
    ),
  );
  expect(
    qc.getQueryData(workflowDebugKeys.detail("ws-b", "run-a")),
  ).toBeUndefined();
  expect(spy).toHaveBeenCalledExactlyOnceWith({
    queryKey: workflowDebugKeys.all("ws-a"),
  });
  hook.unmount();
  qc.clear();
});

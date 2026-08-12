/**
 * @vitest-environment jsdom
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import type { WSClient } from "../api/ws-client";
import type { WSMessage } from "../types/events";
import { workflowKeys, workflowRunKeys } from "../workflows/queries";
import { autopilotKeys } from "../autopilots/queries";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
}));

vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

/**
 * Records the `onAny` handler so a test can push a raw message through the
 * prefix dispatcher, which is the path every `workflow:*` event takes - the
 * engine emits ten distinct names and none of them has a dedicated `ws.on`
 * subscription, by design (see the `workflow:` entry in the refreshMap).
 */
function createRecordingWs(): {
  ws: WSClient;
  emit: (type: string, payload?: unknown) => void;
} {
  let anyHandler: ((msg: WSMessage) => void) | undefined;
  const ws = {
    on: vi.fn(() => () => {}),
    onAny: vi.fn((handler: (msg: WSMessage) => void) => {
      anyHandler = handler;
      return () => {};
    }),
    onReconnect: vi.fn(() => () => {}),
  } as unknown as WSClient;
  return {
    ws,
    emit: (type: string, payload: unknown = {}) =>
      anyHandler?.({ type, payload } as unknown as WSMessage),
  };
}

function createStores(): RealtimeSyncStores {
  return {
    authStore: Object.assign(() => ({}), {
      getState: () => ({ user: { id: "u1" } }),
      subscribe: () => () => {},
      setState: () => {},
      destroy: () => {},
    }),
  } as unknown as RealtimeSyncStores;
}

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

const runListKey = [...workflowRunKeys.list("ws-1"), {}] as const;
const runDetailKey = workflowRunKeys.detail("ws-1", "wfr-1");
const templateDetailKey = workflowKeys.detail("ws-1", "wft-1");

// Every event name the engine's Notifier / handlers can emit. All ten must
// reach the same invalidation: the fine-grained names exist for the server's
// metric labels, not for per-name cache surgery on this side.
const workflowEvents = [
  "workflow:run_changed",
  "workflow:run_started",
  "workflow:run_completed",
  "workflow:run_failed",
  "workflow:run_blocked",
  "workflow:run_cancelled",
  "workflow:step_queued",
  "workflow:step_submitted",
  "workflow:step_blocked",
  "workflow:acceptance_open",
];

describe("useRealtimeSync - workflow run events", () => {
  let qc: QueryClient;

  beforeEach(() => {
    vi.useFakeTimers();
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  });

  afterEach(() => {
    vi.useRealTimers();
    qc.clear();
    vi.clearAllMocks();
  });

  function mount() {
    const rec = createRecordingWs();
    renderHook(() => useRealtimeSync(rec.ws, createStores()), {
      wrapper: createWrapper(qc),
    });
    return rec;
  }

  it.each(workflowEvents)(
    "%s invalidates the run list and the run detail",
    (event) => {
      qc.setQueryData(runListKey, { runs: [], total: 0 });
      qc.setQueryData(runDetailKey, { id: "wfr-1", status: "running" });

      const { emit } = mount();
      emit(event, { run_id: "wfr-1" });
      // The dispatcher debounces per prefix by 100ms.
      vi.advanceTimersByTime(150);

      expect(qc.getQueryState(runListKey)?.isInvalidated).toBe(true);
      expect(qc.getQueryState(runDetailKey)?.isInvalidated).toBe(true);
    },
  );

  it("collapses the burst one engine command produces into a single refresh", () => {
    // One StartRun writes the run, activates the first node and queues its
    // task, emitting several events back-to-back. Without the debounce each
    // one would refetch the same run.
    qc.setQueryData(runDetailKey, { id: "wfr-1", status: "running" });
    const invalidateSpy = vi.spyOn(qc, "invalidateQueries");

    const { emit } = mount();
    emit("workflow:run_started", { run_id: "wfr-1" });
    emit("workflow:step_queued", { run_id: "wfr-1" });
    emit("workflow:run_changed", { run_id: "wfr-1" });
    vi.advanceTimersByTime(150);

    const runCalls = invalidateSpy.mock.calls.filter(
      (call) =>
        (call[0] as { queryKey?: readonly unknown[] })?.queryKey?.[0] ===
        "workflow-runs",
    );
    expect(runCalls).toHaveLength(1);
  });

  it("does not invalidate the template cache - a run is not a graph edit", () => {
    // Runs deliberately live in their own key tree. Nesting them under the
    // template's key would make every step transition mark the editor's cached
    // graph stale, and a run emits many events.
    qc.setQueryData(templateDetailKey, { id: "wft-1", key: "bug_fix" });
    qc.setQueryData(runDetailKey, { id: "wfr-1", status: "running" });

    const { emit } = mount();
    emit("workflow:step_submitted", { run_id: "wfr-1" });
    vi.advanceTimersByTime(150);

    expect(qc.getQueryState(runDetailKey)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(templateDetailKey)?.isInvalidated).toBe(false);
  });

  it("leaves unrelated domains alone", () => {
    const autopilotListKey = autopilotKeys.list("ws-1");
    qc.setQueryData(autopilotListKey, { autopilots: [], total: 0 });
    qc.setQueryData(runDetailKey, { id: "wfr-1", status: "running" });

    const { emit } = mount();
    emit("workflow:run_completed", { run_id: "wfr-1" });
    vi.advanceTimersByTime(150);

    expect(qc.getQueryState(runDetailKey)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(autopilotListKey)?.isInvalidated).toBe(false);
  });

  it("invalidates a run event's siblings even for an event name it has never seen", () => {
    // Forward compatibility: the prefix dispatcher keys on `workflow`, so a
    // future `workflow:step_retried` refreshes the trace without a client
    // release. That is the whole reason this is a prefix map and not a switch.
    qc.setQueryData(runDetailKey, { id: "wfr-1", status: "running" });

    const { emit } = mount();
    emit("workflow:step_retried", { run_id: "wfr-1" });
    vi.advanceTimersByTime(150);

    expect(qc.getQueryState(runDetailKey)?.isInvalidated).toBe(true);
  });
});

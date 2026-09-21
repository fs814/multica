import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type {
  CreateRuntimeCLIRunRequest,
  RuntimeCLIRegistryResult,
  RuntimeCLIRunRequest,
} from "../types";

/**
 * Machine-local CLI directory (TES-140).
 *
 * Both flows are request/poll round trips carried over the daemon heartbeat,
 * so they have the same shape as `resolveRuntimeLocalSkills`: kick off, poll
 * until terminal, then surface the result or throw.
 *
 * The server can take up to its pending timeout to see a claim — the daemon is
 * nudged immediately, but a missed nudge falls back to the next scheduled
 * heartbeat. The client timeout budgets for that rather than pretending the
 * round trip is instant.
 */

export const runtimeCLIKeys = {
  all: () => ["runtimes", "clis"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeCLIKeys.all(), runtimeId] as const,
};

const POLL_INTERVAL_MS = 500;
// Must exceed cliListPendingTimeout + cliListRunningTimeout
// (server/internal/handler/runtime_cli.go) = 30s + 60s, with headroom for
// heartbeat delivery jitter.
const CLI_LIST_POLL_TIMEOUT_MS = 100_000;
// Must exceed cliRunPendingTimeout + the largest possible running bound
// (30s + 600s + 15s grace). The panel passes the entry's own `timeout_seconds`
// back as a hint, so a short-running entry finishes long before this.
const CLI_RUN_POLL_TIMEOUT_MS = 11 * 60_000;

function isTerminal(status: string): boolean {
  return status !== "pending" && status !== "running";
}

export async function resolveRuntimeCLIs(
  runtimeId: string,
): Promise<RuntimeCLIRegistryResult> {
  const initial = await api.initiateListCLIs(runtimeId);
  const start = Date.now();
  let current = initial;

  while (!isTerminal(current.status)) {
    if (Date.now() - start > CLI_LIST_POLL_TIMEOUT_MS) {
      throw new Error("timed out waiting for the machine to report its CLI registry");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getCLIListResult(runtimeId, initial.id);
  }

  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "the machine could not read its CLI registry");
  }

  return {
    entries: current.clis ?? [],
    registryPath: current.registry_path ?? "",
  };
}

/**
 * Run one registry entry and wait for the machine to finish.
 *
 * A non-zero exit code is NOT an error here: the process ran, and its output
 * and status are the result the caller asked for. Only a failure to run
 * (unregistered key, failed parameter validation, timeout, spawn error) throws.
 * Callers render exit codes themselves.
 */
export async function runRuntimeCLI(
  runtimeId: string,
  cliKey: string,
  data: CreateRuntimeCLIRunRequest,
): Promise<RuntimeCLIRunRequest> {
  const initial = await api.initiateCLIRun(runtimeId, cliKey, data);
  const start = Date.now();
  let current = initial;

  while (!isTerminal(current.status)) {
    if (Date.now() - start > CLI_RUN_POLL_TIMEOUT_MS) {
      throw new Error("timed out waiting for the CLI run to finish");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getCLIRunResult(runtimeId, initial.id);
  }

  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "the CLI run failed");
  }

  return current;
}

export function runtimeCLIRegistryOptions(runtimeId: string | null | undefined) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeCLIKeys.forRuntime(runtimeId)
      : runtimeCLIKeys.all(),
    queryFn: () => resolveRuntimeCLIs(runtimeId as string),
    enabled: Boolean(runtimeId),
    staleTime: 30_000,
    retry: false,
  });
}

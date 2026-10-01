import { z } from "zod";
import { parseWithFallback } from "../api/schema";
import { isRuntimeUsableForUser } from "../runtimes/access";
import type { Agent, AgentRuntime, UpdateAgentRequest } from "../types";

// Require the identity/binding fields before deciding that an agent is safe to
// update. A malformed response must never look like an unbound agent.
const bindingSchema = z.object({
  id: z.string().min(1), workspace_id: z.string().min(1),
  owner_id: z.string().nullable(), runtime_id: z.string().nullable(),
  runtime_bound: z.boolean().optional(), archived_at: z.string().nullable(),
});
type Binding = z.infer<typeof bindingSchema>;

export function canAssignMissingRuntime(agent: Pick<Agent, "workspace_id" | "owner_id" | "runtime_id" | "runtime_bound" | "archived_at">, workspaceId: string, userId: string | null) {
  return !!userId && agent.workspace_id === workspaceId && agent.owner_id === userId &&
    !agent.archived_at && !(agent.runtime_id ?? "").trim() && agent.runtime_bound !== true;
}

export interface RuntimeAssignmentResult {
  assigned: string[];
  skipped: string[];
  failed: string[];
}

export async function assignMissingRuntimes(
  input: { workspaceId: string; userId: string; agentIds: string[]; runtime: AgentRuntime; signal: AbortSignal },
  client: { getAgent(id: string): Promise<unknown>; updateAgent(id: string, data: UpdateAgentRequest): Promise<unknown> },
  report?: (result: RuntimeAssignmentResult) => void,
): Promise<RuntimeAssignmentResult> {
  const { workspaceId, userId, runtime, signal } = input;
  if (!userId || runtime.workspace_id !== workspaceId || !isRuntimeUsableForUser(runtime, userId)) {
    throw new Error("Runtime is not available to this account in this workspace");
  }
  const result: RuntimeAssignmentResult = { assigned: [], skipped: [], failed: [] };
  const read = (raw: unknown) => {
    const parsed = bindingSchema.safeParse(raw);
    // Never put the full agent payload (instructions or local configuration)
    // into schema-warning logs when only binding metadata is needed here.
    return parseWithFallback<Binding | null>(parsed.success ? parsed.data : null, bindingSchema, null, { endpoint: "bulk-runtime-assignment" });
  };
  // Deliberately sequential: large imported workspaces must not flood the API.
  // Re-read before each update; retries skip already assigned agents.
  for (const id of new Set(input.agentIds)) {
    signal.throwIfAborted();
    try {
      const agent = read(await client.getAgent(id));
      signal.throwIfAborted();
      if (!agent || agent.id !== id) throw new Error("Invalid agent response");
      if (!canAssignMissingRuntime({ ...agent, runtime_id: agent.runtime_id ?? "" }, workspaceId, userId)) {
        result.skipped.push(id);
      } else {
        const updated = read(await client.updateAgent(id, {
          runtime_id: runtime.id, model: "", thinking_level: "", service_tier: "",
        }));
        if (!updated || updated.id !== id || updated.workspace_id !== workspaceId ||
            updated.runtime_id !== runtime.id || updated.runtime_bound === false) {
          throw new Error("Runtime assignment was not confirmed");
        }
        result.assigned.push(id);
      }
    } catch (error) {
      if (signal.aborted) throw error;
      result.failed.push(id);
    }
    report?.({ assigned: [...result.assigned], skipped: [...result.skipped], failed: [...result.failed] });
  }
  return result;
}

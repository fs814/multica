"use client";

import type { Agent } from "@multica/core/types";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions } from "@multica/core/workspace/queries";
import { executionLocation, runtimeListOptions } from "@multica/core/runtimes";
import { knotToolTarget, parseKnotRuntimeConfig } from "@multica/core/agents";
import { useT } from "../../i18n";

export function ExecutionLocation({ agentId, agent: providedAgent, human = false }: { agentId?: string; agent?: Agent; human?: boolean }) {
  const wsId = useWorkspaceId();
  const { t } = useT("runtimes");
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const agent = providedAgent ?? agents.find(a => a.id === agentId);
  const location = executionLocation(agent, runtimes, Date.now());
  const runtime = runtimes.find(r => r.id === agent?.runtime_id);
  const target = knotToolTarget(agent?.runtime_config);
  const client = target === "client" ? parseKnotRuntimeConfig(agent?.runtime_config).clientUuid : undefined;
  const label = human ? t($ => $.execution.human) : location.state === "bound"
    ? `${location.machineName} · ${location.shortId} · ${location.runtimeLabel} · ${t($ => $.execution.health[location.health])}`
    : t($ => $.execution[location.state]);
  return <span className="block min-w-0 text-caption text-muted-foreground" title={[label, location.state === "bound" ? location.deviceInfo : null].filter(Boolean).join(" · ")}>
    <span className="block truncate">{label}</span>
    {runtime?.provider === "knot-http" && <span className="block truncate" title={[t($ => $.execution.knot_unverified), client].filter(Boolean).join(" · ")}>{t($ => $.execution.knot_label)}: {t($ => $.execution.knot[target])}{client ? ` · ${client.slice(0, 8)}` : ""}</span>}
  </span>;
}

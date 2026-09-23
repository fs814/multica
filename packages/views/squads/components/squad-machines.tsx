"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions, workspaceKeys } from "@multica/core/workspace/queries";
import { runtimeListOptions, summarizeExecutionLocations } from "@multica/core/runtimes";
import { ExecutionLocation } from "../../runtimes/components/execution-location";
import { useT } from "../../i18n";

export function SquadMachines({ squadId, leaderId, compact = false }: { squadId: string; leaderId: string; compact?: boolean }) {
  const wsId = useWorkspaceId();
  const { t } = useT("runtimes");
  const { data: members, isError } = useQuery({ queryKey: [...workspaceKeys.squads(wsId), squadId, "members"], queryFn: () => api.listSquadMembers(squadId), enabled: !!wsId && !!squadId });
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const ids = new Set([leaderId, ...(members ?? []).filter(m => m.member_type === "agent").map(m => m.member_id)]);
  const summary = summarizeExecutionLocations([...ids].map(id => agents.find(a => a.id === id)), runtimes, Date.now());
  const labels = summary.machines.map(m => `${m.label} × ${m.count}`);
  if (summary.unbound) labels.push(`${t($ => $.execution.unbound)} × ${summary.unbound}`);
  if (summary.unavailable) labels.push(`${t($ => $.execution.unavailable)} × ${summary.unavailable}`);
  return <div className="min-w-0 space-y-1 text-caption text-muted-foreground">
    {!compact && <><span>{t($ => $.execution.leader)}</span><ExecutionLocation agentId={leaderId} /></>}
    <p className="break-words">{t($ => $.execution.summary)}: {isError || !members ? t($ => $.execution.unavailable) : labels.join("; ")}</p>
    {!compact && <p>{t($ => $.execution.binding_note)}</p>}
  </div>;
}

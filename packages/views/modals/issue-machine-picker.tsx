"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Monitor, Users } from "lucide-react";
import { Switch } from "@multica/ui/components/ui/switch";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import { deriveRuntimeHealth, runtimeListOptions } from "@multica/core/runtimes";
import { agentListOptions, memberListOptions, squadListOptions, squadMemberStatusOptions } from "@multica/core/workspace/queries";
import type { IssueAssigneeType } from "@multica/core/types";
import { PillButton } from "../common/pill-button";
import { useT } from "../i18n";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import { PropertyPicker, PickerItem, PickerSection } from "../issues/components/pickers/property-picker";
import { buildRuntimeMachines, runtimeRowLabel } from "../runtimes/components/runtime-machines";
import { useLocalDaemonStatus } from "../platform/use-local-daemon-status";
import { useLocalIssueRunner } from "../platform/use-local-issue-runner";

interface Selection {
  assigneeType?: IssueAssigneeType | null;
  assigneeId?: string | null;
}

/** Keep validation in the form so every submit path, including shortcuts, checks it. */
export function useIssueMachineTarget({ assigneeType, assigneeId }: Selection, initialRemoteOnly = false, initialLocal: { directory?: string; provider?: string; useAssignee?: boolean } = {}) {
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id);
  const local = useLocalDaemonStatus();
  const [remoteOnly, setRemoteOnly] = useState(initialRemoteOnly);
  // A remembered Center assignee never opts the user into bound execution.
  const [useLocalAssignee, setUseLocalAssignee] = useState(initialLocal.useAssignee ?? false);
  const localRunner = useLocalIssueRunner(local, initialLocal);
  const discover = !!wsId && !!userId;
  const agentsQuery = useQuery({ ...agentListOptions(wsId), enabled: discover });
  const runtimesQuery = useQuery({ ...runtimeListOptions(wsId), enabled: discover });
  const membersQuery = useQuery({ ...memberListOptions(wsId), enabled: discover });
  const squadsQuery = useQuery({ ...squadListOptions(wsId), enabled: discover });
  const squadMembersQuery = useQuery({ ...squadMemberStatusOptions(wsId, assigneeId ?? ""), enabled: discover && !remoteOnly && useLocalAssignee && assigneeType === "squad" && !!assigneeId });
  const agents = agentsQuery.data ?? [];
  const runtimes = runtimesQuery.data ?? [];
  const role = membersQuery.data?.find((member) => member.user_id === userId)?.role;
  const loading = agentsQuery.isPending || runtimesQuery.isPending || membersQuery.isPending || squadsQuery.isPending;
  const failed = agentsQuery.isError || runtimesQuery.isError || membersQuery.isError || squadsQuery.isError;
  const now = Date.now();
  const machines = buildRuntimeMachines(runtimes, { now, currentUserId: userId, localDaemonId: local.daemonId, localMachineName: local.deviceName });
  const leaderId = assigneeType === "squad"
    ? squadsQuery.data?.find((squad) => squad.id === assigneeId)?.leader_id
    : undefined;
  const selectedAgent = agents.find((agent) => agent.id === (assigneeType === "agent" ? assigneeId : leaderId));
  const selectedMachine = machines.find((machine) => machine.runtimes.some((runtime) => runtime.id === selectedAgent?.runtime_id));
  const selectedRuntime = runtimes.find((runtime) => runtime.id === selectedAgent?.runtime_id);
  const selectedOffline = selectedRuntime && deriveRuntimeHealth(selectedRuntime, now) !== "online";
  const eligibleAgents = agents.filter((agent) => !agent.archived_at && isAgentRuntimeBound(agent) && canAssignAgentToIssue(agent, {
    userId: userId ?? null,
    role: role === "owner" || role === "admin" || role === "member" ? role : null,
  }).allowed);
  const eligibleSquads = (squadsQuery.data ?? []).filter((squad) => !squad.archived_at && eligibleAgents.some((agent) => agent.id === squad.leader_id));
  const isRemote = (machine: typeof machines[number]) => !!local.daemonId && !!machine.daemonId && !machine.isCurrent && machine.daemonId !== local.daemonId;
  const isLocal = (machine: typeof machines[number]) => !!local.daemonId && machine.daemonId === local.daemonId;
  const localExecution = !remoteOnly && !useLocalAssignee;
  const squadLocal = assigneeType !== "squad" || (!squadMembersQuery.isPending && !squadMembersQuery.isError
    && (squadMembersQuery.data?.members.length ?? 0) > 0
    && squadMembersQuery.data?.members.every(member => member.member_type === "member" || (member.member_type === "agent" && eligibleAgents.some(agent => agent.id === member.member_id && runtimes.some(runtime => runtime.id === agent.runtime_id && runtime.daemon_id === local.daemonId)))));
  const executionBlocked = localExecution ? !localRunner.ready : (loading || failed || !selectedMachine || !(remoteOnly ? isRemote(selectedMachine) : isLocal(selectedMachine) && local.running && squadLocal)
    || !eligibleAgents.some((agent) => agent.id === selectedAgent?.id)
    || (assigneeType === "squad" && !eligibleSquads.some((squad) => squad.id === assigneeId)));
  return {
    assigneeType, assigneeId, remoteOnly, setRemoteOnly, executionBlocked, local,
    localExecution, localRunner, useLocalAssignee, setUseLocalAssignee,
    loading, failed, now, machines: machines.filter(remoteOnly ? isRemote : isLocal),
    selectedMachine, selectedOffline, selectedAgent, eligibleAgents, eligibleSquads,
    retry: () => { void agentsQuery.refetch(); void runtimesQuery.refetch(); void membersQuery.refetch(); void squadsQuery.refetch(); },
    async validateBoundTarget() {
      if (remoteOnly || localExecution) return;
      const [freshAgents, freshRuntimes, freshMembers, freshSquads, freshSquadMembers] = await Promise.all([
        agentsQuery.refetch(), runtimesQuery.refetch(), membersQuery.refetch(), squadsQuery.refetch(),
        assigneeType === "squad" ? squadMembersQuery.refetch() : Promise.resolve(null),
      ]);
      if ([freshAgents, freshRuntimes, freshMembers, freshSquads, freshSquadMembers].some(result => result?.isError)) throw new Error("Cannot verify the local agent or squad");
      const currentLocal = await localRunner.getCurrentStatus();
      if (currentLocal?.state !== "running" || !currentLocal.daemonId || currentLocal.daemonId !== local.daemonId) throw new Error("Local daemon identity changed");
      const freshRole = freshMembers.data?.find(member => member.user_id === userId)?.role;
      const boundHere = (id: string | undefined) => freshAgents.data?.some(agent => agent.id === id && !agent.archived_at && isAgentRuntimeBound(agent)
        && canAssignAgentToIssue(agent, { userId: userId ?? null, role: freshRole === "owner" || freshRole === "admin" || freshRole === "member" ? freshRole : null }).allowed
        && freshRuntimes.data?.some(runtime => runtime.id === agent.runtime_id && runtime.daemon_id === currentLocal.daemonId));
      const squad = freshSquads.data?.find(squad => squad.id === assigneeId && !squad.archived_at);
      if (!boundHere(assigneeType === "agent" ? assigneeId ?? undefined : squad?.leader_id)
        || (assigneeType === "squad" && (!freshSquadMembers?.data?.members.length || !freshSquadMembers.data.members.every(member => member.member_type === "member" || (member.member_type === "agent" && boundHere(member.member_id)))))) {
        throw new Error("Selected agent or squad is no longer bound entirely to this machine");
      }
    },
  };
}

/** A target choice sets the assignee; it never rebinds a shared agent. */
export function IssueMachinePicker({ target, onSelect, mode = "manual" }: {
  target: ReturnType<typeof useIssueMachineTarget>;
  onSelect: (type: "agent" | "squad", id: string) => void;
  mode?: "manual" | "agent";
}) {
  const { t } = useT("modals");
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const { assigneeType, assigneeId, selectedMachine, selectedOffline, selectedAgent, loading, failed, eligibleAgents, eligibleSquads, machines, now } = target;
  const label = !target.remoteOnly ? t(($) => $.create_issue.machine.local_target_label)
    : mode === "agent" ? t(($) => $.create_issue.machine.creation_label) : t(($) => $.create_issue.machine.label);
  const selectedLabel = selectedMachine
    ? `${selectedMachine.title}${selectedOffline ? ` · ${t(($) => $.create_issue.machine.offline)}` : ""}`
    : selectedAgent?.runtime_id
      ? t(($) => $.create_issue.machine.unavailable)
      : t(($) => $.create_issue.machine.unselected);
  // The machine is a fixed mode, not a derived label from a remembered remote actor.
  const localSelectedName = machines.some(machine => machine.id === selectedMachine?.id)
    ? (assigneeType === "squad" ? eligibleSquads.find(squad => squad.id === assigneeId)?.name : eligibleAgents.find(agent => agent.id === assigneeId)?.name)
    : undefined;
  const pickerValue = loading ? t(($) => $.create_issue.machine.loading)
    : failed ? t(($) => $.create_issue.machine.error)
      : target.remoteOnly ? selectedLabel : localSelectedName || t(($) => $.create_issue.machine.unselected);
  const query = search.trim().toLowerCase();
  const groups = machines.map((machine) => ({
    machine,
    choices: machine.runtimes.flatMap((runtime) => eligibleAgents
      .filter((agent) => agent.runtime_id === runtime.id)
      .flatMap((agent) => [
        { id: agent.id, name: agent.name, type: "agent" as const, runtime },
        ...eligibleSquads.filter((squad) => squad.leader_id === agent.id).map((squad) => ({ id: squad.id, name: squad.name, type: "squad" as const, runtime })),
      ]))
      .filter(({ name, runtime }) => {
        const haystack = `${machine.title} ${runtimeRowLabel(runtime, machine.title)} ${name}`;
        return haystack.toLowerCase().includes(query) || matchesPinyin(haystack, query);
      }),
  })).filter(({ choices }) => choices.length > 0);

  return (
    <>
    <label className="flex items-center gap-1.5 text-caption">
      <Switch checked={target.remoteOnly} onCheckedChange={target.setRemoteOnly} />
      {t(($) => $.create_issue.machine.remote_only)}
    </label>
    {!target.remoteOnly && <div className="w-full space-y-2 rounded-md border p-3">
      <p className="flex items-center gap-1.5 text-caption"><Monitor className="size-3.5 shrink-0" /><span>{t(($) => $.create_issue.machine.label)}: {t(($) => $.create_issue.machine.local_value)}</span></p>
      <p className="text-caption">{t(($) => $.create_issue.machine.local_machine)}: {target.local.deviceName || t(($) => $.create_issue.machine.local_machine)}</p>
      <label className="flex items-center gap-2 text-caption"><Switch checked={target.useLocalAssignee} onCheckedChange={target.setUseLocalAssignee} />{t(($) => $.create_issue.machine.local_assignee)}</label>
      {target.localExecution && <p className="text-caption text-muted-foreground">{t(($) => $.create_issue.machine.local_direct_hint)}</p>}
      {target.localExecution && <>
      <div className="flex gap-2">
        <Input aria-label={t(($) => $.create_issue.machine.local_directory)} placeholder={target.localRunner.defaultDirectory || t(($) => $.create_issue.machine.local_directory)} value={target.localRunner.directory} onChange={event => target.localRunner.setDirectory(event.target.value)} />
        <Button variant="outline" onClick={() => void target.localRunner.browse()}>{t(($) => $.create_issue.machine.browse)}</Button>
      </div>
      <label className="flex items-center gap-2 text-caption">{t(($) => $.create_issue.machine.local_cli)}
        <select className="min-w-0 flex-1 rounded-md border bg-background p-2" value={target.localRunner.provider} onChange={event => target.localRunner.setProvider(event.target.value)}>
          <option value="">{t(($) => $.create_issue.machine.default_cli)}</option>
          {target.localRunner.providers.map(provider => <option key={provider} value={provider}>{provider}</option>)}
        </select>
      </label>
      </>}
      {target.executionBlocked && <p role="status" className="text-caption text-muted-foreground">{target.localExecution ? t(($) => $.create_issue.machine.local_ready_hint) : t(($) => $.create_issue.machine.local_required)}</p>}
      {target.localRunner.error && <p role="alert" className="text-caption text-destructive">{target.localRunner.error}</p>}
    </div>}
    {!target.localExecution && <div className={target.remoteOnly ? "contents" : "w-full space-y-2"}><PropertyPicker
      open={open}
      onOpenChange={setOpen}
      align="start"
      width="w-80"
      searchable
      searchPlaceholder={t(($) => $.create_issue.machine.search)}
      onSearchChange={setSearch}
      triggerRender={<PillButton aria-label={`${label}: ${pickerValue}`} />}
      trigger={<>{target.remoteOnly ? <Monitor className="size-3.5 shrink-0" /> : <Users className="size-3.5 shrink-0" />}<span className="truncate">{label}: {pickerValue}</span></>}
      header={<p className="px-2 py-1.5 text-caption text-muted-foreground">{!target.remoteOnly ? t(($) => $.create_issue.machine.local_target_hint) : mode === "agent" ? t(($) => $.create_issue.machine.creation_hint) : t(($) => $.create_issue.machine.hint)}{target.remoteOnly && assigneeType === "squad" && <> {t(($) => $.create_issue.machine.squad_hint)}</>}</p>}
    >
      {loading ? <p className="p-2 text-caption text-muted-foreground">{t(($) => $.create_issue.machine.loading)}</p>
        : failed ? <div className="p-2 text-caption"><p role="alert">{t(($) => $.create_issue.machine.error)}</p><button type="button" className="mt-2 underline" onClick={target.retry}>{t(($) => $.create_issue.machine.retry)}</button></div>
        : groups.length === 0 ? <p className="p-2 text-caption text-muted-foreground">{query ? t(($) => $.create_issue.machine.no_results) : target.remoteOnly ? t(($) => $.create_issue.machine.empty) : t(($) => $.create_issue.machine.local_empty)}</p>
        : groups.map(({ machine, choices }) => (
          <PickerSection key={machine.id} label={machine.title}>
            {(["agent", "squad"] as const).map(type => {
              const items = choices.filter(choice => choice.type === type)
                .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }) || a.id.localeCompare(b.id));
              if (items.length === 0) return null;
              return <PickerSection key={type} label={type === "agent" ? t(($) => $.create_issue.agent.agents_group) : t(($) => $.create_issue.agent.squads_group)}>
                {items.map(({ id, name, runtime }) => (
                  <PickerItem key={id} selected={assigneeType === type && assigneeId === id} onClick={() => { onSelect(type, id); setOpen(false); }}>
                    <span className="min-w-0 flex-1 truncate">{name} · {type === "squad" ? t(($) => $.create_issue.machine.squad) : runtimeRowLabel(runtime, machine.title)}</span>
                    <span className="shrink-0 text-micro text-muted-foreground">{deriveRuntimeHealth(runtime, now) === "online" ? t(($) => $.create_issue.machine.online) : t(($) => $.create_issue.machine.offline)}</span>
                  </PickerItem>
                ))}
              </PickerSection>;
            })}
          </PickerSection>
        ))}
    </PropertyPicker>
      {!target.remoteOnly && <p className="text-caption text-muted-foreground">{t(($) => $.create_issue.machine.local_bound_hint)}</p>}
    </div>}
    {target.remoteOnly && <p role={target.executionBlocked ? "alert" : "status"} className="w-full text-caption text-muted-foreground">
      {!target.local.daemonId ? t(($) => $.create_issue.machine.identity_required)
        : target.executionBlocked ? (target.remoteOnly ? t(($) => $.create_issue.machine.remote_required) : t(($) => $.create_issue.machine.local_required))
          : target.remoteOnly ? t(($) => $.create_issue.machine.remote_hint) : t(($) => $.create_issue.machine.local_hint)}
    </p>}
    </>
  );
}

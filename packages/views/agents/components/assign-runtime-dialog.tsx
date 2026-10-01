"use client";

import { useState } from "react";
import type { Agent, AgentRuntime, MemberWithUser } from "@multica/core/types";
import { canAssignMissingRuntime, useAssignMissingRuntimes, type RuntimeAssignmentResult } from "@multica/core/agents";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";
import { RuntimePicker } from "./inspector/runtime-picker";

export function AssignRuntimeDialog({ agents, runtimes, members, currentUserId, disabled, onComplete, onBusyChange }: {
  agents: Agent[];
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  disabled: boolean;
  onComplete: () => void;
  onBusyChange: (busy: boolean) => void;
}) {
  const { t } = useT("agents");
  const workspaceId = useWorkspaceId();
  const mutation = useAssignMissingRuntimes(workspaceId, currentUserId);
  const [open, setOpen] = useState(false);
  const [targets, setTargets] = useState<Agent[]>([]);
  const [skipped, setSkipped] = useState(0);
  const [runtimeId, setRuntimeId] = useState("");
  const [result, setResult] = useState<RuntimeAssignmentResult | null>(null);
  const eligible = agents.filter(agent => canAssignMissingRuntime(agent, workspaceId, currentUserId));
  const localRuntimes = runtimes.filter(runtime => runtime.workspace_id === workspaceId);
  const selected = localRuntimes.find(runtime => runtime.id === runtimeId);
  const usable = !!currentUserId && !!selected && isRuntimeUsableForUser(selected, currentUserId);
  const hasRuntime = !!currentUserId && localRuntimes.some(runtime => isRuntimeUsableForUser(runtime, currentUserId));
  const changeOpen = (next: boolean) => {
    if (mutation.isPending) return;
    if (next) {
      setTargets(eligible);
      setSkipped(agents.length - eligible.length);
      setRuntimeId("");
      setResult(null);
      mutation.reset();
    }
    setOpen(next);
  };
  const apply = async () => {
    if (!selected || !usable || mutation.isPending) return;
    // Selection is frozen at confirmation-dialog open. Successful agents are
    // excluded from explicit retries; only the failed subset is submitted again.
    const previous = result;
    const merge = (next: RuntimeAssignmentResult): RuntimeAssignmentResult => ({
      assigned: [...new Set([...(previous?.assigned ?? []), ...next.assigned])],
      skipped: [...new Set([...(previous?.skipped ?? []), ...next.skipped])],
      failed: next.failed,
    });
    onBusyChange(true);
    try {
      const next = await mutation.mutateAsync({
        agentIds: previous ? previous.failed : targets.map(agent => agent.id),
        runtime: selected,
        report: value => setResult(merge(value)),
      });
      setResult(merge(next));
      if (next.failed.length === 0) {
        setOpen(false);
        onComplete();
      }
    } catch {
      // The mutation error stays visible; successful writes are not rolled back.
    } finally {
      onBusyChange(false);
    }
  };
  return <Dialog open={open} onOpenChange={changeOpen}>
    <DialogTrigger render={<Button variant="ghost" size="sm" disabled={disabled || eligible.length === 0} />}>
      {t(($) => $.runtime_assignment.action)}
    </DialogTrigger>
    <DialogContent className="sm:max-w-lg" showCloseButton={!mutation.isPending}>
      <DialogHeader>
        <DialogTitle>{t(($) => $.runtime_assignment.action)}</DialogTitle>
        <DialogDescription>{t(($) => $.runtime_assignment.description)}</DialogDescription>
      </DialogHeader>
      <p className="text-body">{t(($) => $.runtime_assignment.scope, { total: targets.length, skipped })}</p>
      <ul className="max-h-32 overflow-auto text-caption" aria-label={t(($) => $.runtime_assignment.agents)}>
        {targets.map(agent => <li key={agent.id}>{agent.name}</li>)}
      </ul>
      <RuntimePicker value={runtimeId} runtimes={localRuntimes} members={members} currentUserId={currentUserId} variant="field" canEdit={!mutation.isPending} onChange={setRuntimeId} />
      {!hasRuntime && <p role="status" className="text-body">{t(($) => $.runtime_assignment.empty)}</p>}
      {result && <p role={result.failed.length && !mutation.isPending ? "alert" : "status"} className="text-body">{t(($) => $.runtime_assignment.progress, { assigned: result.assigned.length, skipped: result.skipped.length + skipped, failed: result.failed.length })}</p>}
      {!!result?.failed.length && !mutation.isPending && <p className="text-body">{t(($) => $.runtime_assignment.retry_hint)}</p>}
      {mutation.isError && <p role="alert" className="text-body text-destructive">{t(($) => $.runtime_assignment.error)}</p>}
      <DialogFooter>
        <Button variant="outline" disabled={mutation.isPending} onClick={() => changeOpen(false)}>{t(($) => $.row_actions.archive_dialog_cancel)}</Button>
        <Button disabled={mutation.isPending || !usable || targets.length === 0} aria-busy={mutation.isPending} onClick={() => void apply()}>
          {t(($) => mutation.isPending ? $.runtime_assignment.assigning : result?.failed.length ? $.runtime_assignment.retry : $.runtime_assignment.confirm)}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}

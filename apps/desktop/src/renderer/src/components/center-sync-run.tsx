import { useEffect, useId, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getApi } from "@multica/core/api";
import { syncCenters, validateCenterSyncSourceRequest, type CenterSyncProgress } from "@multica/core/api/center-sync";
import type { CenterSyncSession } from "@multica/core/api/center-sync-session";
import { centerSyncWorkspaceListOptions } from "@multica/core/api/center-sync-workspaces";
import { useT } from "@multica/views/i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Progress } from "@multica/ui/components/ui/progress";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";

interface Props {
  sourceAddress: string;
  session: CenterSyncSession;
  disabled: boolean;
  onBusyChange: (busy: boolean) => void;
  onExpired: () => void;
  sourceSession?: CenterSyncSession;
  onSourceExpired?: () => void;
}

export function CenterSyncRun({ sourceAddress, session, disabled, onBusyChange, onExpired, sourceSession, onSourceExpired }: Props) {
  const { t } = useT("settings");
  const queryClient = useQueryClient();
  const [connection] = useState(() => crypto.randomUUID());
  const workspaces = useQuery(centerSyncWorkspaceListOptions(sourceAddress, connection, sourceSession));
  useEffect(() => {
    if (workspaces.isError && sourceSession && !sourceSession.currentUser) onSourceExpired?.();
  }, [workspaces.isError, sourceSession, onSourceExpired]);
  const workspaceLabel = useId();
  const prerequisiteId = useId();
  const [workspace, setWorkspace] = useState<string | null>("all");
  const selectedWorkspace = workspace === "all" ? "all" : workspaces.data?.some(item => item.id === workspace) ? workspace : null;
  const httpsReady = sourceAddress.startsWith("https:") && session.origin.startsWith("https:");
  const [progress, setProgress] = useState<CenterSyncProgress | null>(null);
  const controller = useRef<AbortController | null>(null);
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const active = new AbortController(); lifetime.current = active;
    return () => active.abort();
  }, []);
  const mutation = useMutation({
    retry: false,
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["workspaces"] }); },
    onMutate: () => setProgress({ phase: "checking", batches: 0, records: 0, edits: 0 }),
    mutationFn: async () => {
      if (!httpsReady || !selectedWorkspace || !workspaces.isSuccess || workspaces.isFetching || controller.current) throw new Error("Check server addresses and select an available source workspace");
      const api = sourceSession ? null : getApi();
      if (!lifetime.current || lifetime.current.signal.aborted) throw new Error("Sync settings closed");
      const credential = api?.getToken();
      const sourceOwner = sourceSession?.currentUser;
      const owner = session.currentUser;
      if (!owner || (sourceSession ? sourceSession.origin !== sourceAddress || !sourceOwner : api?.getBaseUrl().replace(/\/$/, "") !== sourceAddress)) throw new Error("Server connection changed; reconnect before syncing");
      const active = new AbortController(); controller.current = active;
      const signal = AbortSignal.any([active.signal, lifetime.current.signal]);
      const checkSessions = () => {
        signal.throwIfAborted();
        if ((sourceSession ? sourceSession.currentUser !== sourceOwner : getApi() !== api || api?.getToken() !== credential) || session.currentUser !== owner) throw new Error("Server login changed; start a new sync run");
      };
      try {
        return await syncCenters({
          origin: sourceAddress,
          request: (action, body, signal) => {
            checkSessions();
            const request = validateCenterSyncSourceRequest({ action, body });
            if (sourceSession) return sourceSession.syncRequest(request.action, request.body, signal);
            return api!.centerSyncSourceRequest(request, signal);
          },
        }, {
          origin: session.origin,
          request: (action, body, signal) => {
            checkSessions();
            return session.syncRequest(action, body, signal);
          },
        }, selectedWorkspace, signal, next => {
          if (!signal.aborted && controller.current === active) setProgress(next);
        }, true);
      } finally {
        controller.current = null;
        if (!session.currentUser) onExpired();
        if (sourceSession && !sourceSession.currentUser) onSourceExpired?.();
      }
    },
  });
  useEffect(() => () => controller.current?.abort(), []);
  useEffect(() => {
    onBusyChange(mutation.isPending);
    return () => onBusyChange(false);
  }, [mutation.isPending, onBusyChange]);
  const items = [{ value: "all", label: t(($) => $.desktop.center.sync_all_workspaces) }, ...(workspaces.data ?? []).map(item => ({ value: item.id, label: item.name }))];
  const result = mutation.data;
  const phase = mutation.isSuccess ? "complete" : progress?.phase ?? "checking";
  const stages = { checking: 0, preparing: 1, pulling: 2, pushing: 3, verifying: 4, complete: 5 };
  const phaseLabels = {
    checking: t(($) => $.desktop.center.sync_progress_checking),
    preparing: t(($) => $.desktop.center.sync_progress_preparing),
    pulling: t(($) => $.desktop.center.sync_progress_pulling),
    pushing: t(($) => $.desktop.center.sync_progress_pushing),
    verifying: t(($) => $.desktop.center.sync_progress_verifying),
    complete: t(($) => $.desktop.center.sync_progress_complete),
  };
  const stage = phase === "complete" ? phaseLabels.complete : t(($) => $.desktop.center.sync_progress_stage, { current: stages[phase] + 1, total: 5, stage: phaseLabels[phase] });
  const progressLabel = mutation.isError ? t(($) => $.desktop.center.sync_progress_stopped, { stage }) : stage;

  return <div className="min-w-0 space-y-3">
    <p className="break-all text-caption text-muted-foreground">{sourceAddress} ↔ {session.origin}</p>
    <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_limits)}</p>
    <span id={workspaceLabel} className="block text-body">{t(($) => $.desktop.center.sync_workspace)}</span>
    <Select items={items} value={selectedWorkspace} disabled={disabled || mutation.isPending || !workspaces.isSuccess || workspaces.isFetching || !items.length} onValueChange={value => { setWorkspace(value); mutation.reset(); setProgress(null); }}>
      <SelectTrigger aria-labelledby={workspaceLabel} className="w-full min-w-0">
        <SelectValue className="min-w-0 truncate" placeholder={t(($) => $.desktop.center.sync_workspace)} />
      </SelectTrigger>
      <SelectContent align="start" alignItemWithTrigger={false}>{items.map(item => <SelectItem key={item.value} value={item.value}><span className="truncate">{item.label}</span></SelectItem>)}</SelectContent>
    </Select>
    {workspaces.isFetching && <p role="status" className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_workspace_loading)}</p>}
    {workspaces.isSuccess && !workspaces.isFetching && !workspaces.data?.length && <p role="status" className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_workspace_empty)}</p>}
    {workspaces.isError && <p role="alert" className="text-body text-destructive">{t(($) => $.desktop.center.sync_workspace_error)}</p>}
    <Button variant="outline" disabled={disabled || mutation.isPending || workspaces.isFetching} onClick={() => void workspaces.refetch()}>{t(($) => $.desktop.center.sync_workspace_refresh)}</Button>
    {!httpsReady && <p id={prerequisiteId} className="break-words text-body text-destructive">{t(($) => $.desktop.center.sync_https_required)}</p>}
    <div className="flex flex-wrap gap-2">
      <Button className="h-auto min-h-[var(--button-height-default)] max-w-full whitespace-normal" disabled={disabled || mutation.isPending || !selectedWorkspace || !workspaces.isSuccess || workspaces.isFetching || !httpsReady} aria-describedby={!httpsReady ? prerequisiteId : undefined} aria-busy={mutation.isPending} onClick={() => mutation.mutate()}>
        {t(($) => $.desktop.center.sync_data)}
      </Button>
      {mutation.isPending && <Button variant="outline" onClick={() => controller.current?.abort()}>{t(($) => $.desktop.center.recovery_cancel)}</Button>}
    </div>
    {progress && <div className="space-y-2">
      <p role={mutation.isPending ? "status" : undefined} className="text-body">{progressLabel}</p>
      <Progress value={mutation.isSuccess ? 5 : progress.percent === undefined ? stages[phase] : progress.percent / 20} max={5} aria-label={t(($) => $.desktop.center.sync_progress)} aria-valuetext={progressLabel} />
      <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_progress_counts, { batches: progress.batches, records: progress.records, edits: progress.edits })}</p>
    </div>}
    {mutation.isError && <p role="alert" className="break-words text-body text-destructive">{t(($) => $.desktop.center.sync_run_error)} {mutation.error.message}</p>}
    {result && <>
      <p role="status" className="text-body">{t(($) => $.desktop.center.sync_result, { workspaces: result.workspaces?.length ?? 1, conflicts: result.conflicts })}</p>
      <details>
        <summary className="cursor-pointer text-body">{t(($) => $.desktop.center.sync_preview)}</summary>
        <pre className="max-h-64 max-w-full overflow-auto whitespace-pre-wrap break-all text-caption">{JSON.stringify({ conflicts: result.review ?? [], records: result.records.slice(0, 50) }, null, 2)}</pre>
      </details>
    </>}
  </div>;
}

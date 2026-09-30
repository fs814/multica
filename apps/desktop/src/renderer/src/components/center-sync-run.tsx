import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getApi } from "@multica/core/api";
import { syncCenters, validateCenterSyncSourceRequest } from "@multica/core/api/center-sync";
import type { CenterSyncSession } from "@multica/core/api/center-sync-session";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { useT } from "@multica/views/i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";

interface Props {
  sourceAddress: string;
  session: CenterSyncSession;
  disabled: boolean;
  onBusyChange: (busy: boolean) => void;
  onExpired: () => void;
}

export function CenterSyncRun({ sourceAddress, session, disabled, onBusyChange, onExpired }: Props) {
  const { t } = useT("settings");
  const workspaces = useQuery({ ...workspaceListOptions(), retry: false });
  const [workspace, setWorkspace] = useState<string | null>(null);
  const controller = useRef<AbortController | null>(null);
  const lifetime = useRef<AbortController | null>(null);
  useEffect(() => {
    const active = new AbortController(); lifetime.current = active;
    return () => active.abort();
  }, []);
  const mutation = useMutation({
    retry: false,
    mutationFn: async () => {
      if (!workspace || controller.current) throw new Error("Select a workspace; wait for the current run to finish");
      const api = getApi();
      if (!lifetime.current || lifetime.current.signal.aborted) throw new Error("Sync settings closed");
      const credential = api.getToken();
      const owner = session.currentUser;
      if (api.getBaseUrl().replace(/\/$/, "") !== sourceAddress || !owner) throw new Error("Server connection changed; reconnect before syncing");
      const active = new AbortController(); controller.current = active;
      const signal = AbortSignal.any([active.signal, lifetime.current.signal]);
      const checkSessions = () => {
        signal.throwIfAborted();
        if (getApi() !== api || api.getToken() !== credential || session.currentUser !== owner) throw new Error("Server login changed; start a new sync run");
      };
      try {
        return await syncCenters({
          origin: sourceAddress,
          request: (action, body, signal) => {
            checkSessions();
            return api.centerSyncSourceRequest(validateCenterSyncSourceRequest({ action, body }), signal);
          },
        }, {
          origin: session.origin,
          request: (action, body, signal) => {
            checkSessions();
            return session.syncRequest(action, body, signal);
          },
        }, workspace, signal);
      } finally {
        controller.current = null;
        if (!session.currentUser) onExpired();
      }
    },
  });
  useEffect(() => () => controller.current?.abort(), []);
  useEffect(() => {
    onBusyChange(mutation.isPending);
    return () => onBusyChange(false);
  }, [mutation.isPending, onBusyChange]);
  const items = (workspaces.data ?? []).map(item => ({ value: item.id, label: item.name }));
  const result = mutation.data;

  return <div className="space-y-3">
    <p className="break-all text-caption text-muted-foreground">{sourceAddress} → {session.origin}</p>
    <Select items={items} value={workspace} disabled={disabled || mutation.isPending} onValueChange={value => { setWorkspace(value); mutation.reset(); }}>
      <SelectTrigger aria-label={t(($) => $.desktop.center.sync_workspace)} className="w-full">
        <SelectValue placeholder={t(($) => $.desktop.center.sync_workspace)} />
      </SelectTrigger>
      <SelectContent>{items.map(item => <SelectItem key={item.value} value={item.value}>{item.label}</SelectItem>)}</SelectContent>
    </Select>
    {workspaces.isError && <p role="alert" className="text-body text-destructive">{t(($) => $.desktop.center.sync_workspace_error)}</p>}
    <div className="flex flex-wrap gap-2">
      <Button className="h-auto min-h-[var(--button-height-default)] max-w-full whitespace-normal" disabled={disabled || mutation.isPending || !workspace} aria-busy={mutation.isPending} onClick={() => mutation.mutate()}>
        {t(($) => $.desktop.center.sync_data)}
      </Button>
      {mutation.isPending && <Button variant="outline" onClick={() => controller.current?.abort()}>{t(($) => $.desktop.center.recovery_cancel)}</Button>}
    </div>
    {mutation.isError && <p role="alert" className="break-words text-body text-destructive">{t(($) => $.desktop.center.sync_run_error)} {mutation.error.message}</p>}
    {result && <>
      <p role="status" className="text-body">{t(($) => $.desktop.center.sync_result, { records: result.records.length, cursor: result.cursor, conflicts: result.conflicts, pending: result.pending })}</p>
      <details>
        <summary className="cursor-pointer text-body">{t(($) => $.desktop.center.sync_preview)}</summary>
        <pre className="max-h-64 max-w-full overflow-auto whitespace-pre-wrap break-all text-caption">{JSON.stringify(result.records.slice(0, 50), null, 2)}</pre>
      </details>
    </>}
  </div>;
}

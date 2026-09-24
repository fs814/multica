import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { remoteIssueTargetsOptions, useCreateRemoteIssue } from "@multica/core/issues";
import { setCurrentWorkspace } from "@multica/core/platform";
import { DragStrip } from "@multica/views/platform";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import type { LocalCapabilities, LocalIssue } from "../../../shared/local-issue";
import { CenterSettingsTab, LocalDaemonConnection } from "./center-settings-tab";

/** Local issue list and optional creation form for an offline daemon. */
export function LocalIssuesHome({ centerAvailable, centerUserId, onOpenCenter, onBack, embedded = false }: {
  centerAvailable: boolean;
  centerUserId: string | null;
  onOpenCenter: () => void;
  onBack?: () => void;
  embedded?: boolean;
}) {
  const [issues, setIssues] = useState<LocalIssue[]>([]);
  const [selectedIssueId, setSelectedIssueId] = useState<string | null>(null);
  const [remoteOnly, setRemoteOnly] = useState(false);
  const [machine, setMachine] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [directory, setDirectory] = useState("");
  const [defaultDirectory, setDefaultDirectory] = useState("");
  const [provider, setProvider] = useState("");
  const [agents, setAgents] = useState<string[]>([]);
  const agentsKey = agents.join(",");
  const [capabilities, setCapabilities] = useState<LocalCapabilities | null>(null);
  const [daemonId, setDaemonId] = useState<string | undefined>();
  const [daemonRunning, setDaemonRunning] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [showIssueForm, setShowIssueForm] = useState(false);
  const [showCenterSettings, setShowCenterSettings] = useState(false);
  const remoteQuery = useQuery({
    ...remoteIssueTargetsOptions(centerUserId, daemonId),
    enabled: remoteOnly && centerAvailable && !!centerUserId && !!daemonId,
  });
  const remoteChoices = remoteQuery.data ?? [];
  const createRemote = useCreateRemoteIssue();
  const remoteReady = centerAvailable && !!centerUserId && !!daemonId && !remoteQuery.isPending && !remoteQuery.isError;

  useEffect(() => {
    let live = true;
    const refresh = async () => {
      try {
        const status = await window.daemonAPI.getStatus();
        if (!live) return;
        setDaemonRunning(status.state === "running");
        setAgents(status.agents ?? []);
        setDaemonId(status.daemonId);
        const defaultPath = await window.daemonAPI.getLocalIssueDefaultDirectory?.();
        if (live && defaultPath) setDefaultDirectory(defaultPath);
        if (status.state !== "running") {
          setIssues([]);
          return;
        }
        const next = await window.daemonAPI.listLocalIssues();
        if (live) {
          setIssues(next);
          setSelectedIssueId(current => current && next.some(issue => issue.id === current) ? current : next[0]?.id ?? null);
        }
      } catch (cause) {
        if (live) setError(cause instanceof Error ? cause.message : String(cause));
      }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 3000);
    return () => { live = false; clearInterval(timer); };
  }, []);

  useEffect(() => {
    if (!agentsKey) { setCapabilities(null); return; }
    let live = true;
    void window.daemonAPI.getLocalCapabilities(provider).then(value => {
      if (live) setCapabilities(value);
    }).catch(() => { if (live) setCapabilities(null); });
    return () => { live = false; };
  }, [agentsKey, provider]);

  async function createIssue() {
    setError("");
    setBusy(true);
    try {
      if (!remoteOnly) {
        if (!daemonRunning) throw new Error("Local daemon is not running");
        const issue = await window.daemonAPI.createLocalIssue({ title, description, directory, provider, machine: "local" });
        setIssues(current => [issue, ...current]);
        setSelectedIssueId(issue.id);
      } else {
        if (!remoteReady) throw new Error("Remote execution is unavailable; local execution will not be used");
        // Revalidate discovery before dispatch. Failure never enters the local path.
        const refreshed = await remoteQuery.refetch();
        if (refreshed.isError) throw new Error("Could not refresh remote targets; local execution will not be used");
        const choice = refreshed.data?.find(item => item.id === machine);
        if (!choice) throw new Error("Selected remote machine is no longer available");
        // Center's existing agent-to-runtime binding is authoritative for
        // machine routing. No local issue data is sent for the default path.
        await createRemote.mutateAsync({ workspaceSlug: choice.workspaceSlug, data: { title, description, status: "todo", assignee_type: choice.assigneeType, assignee_id: choice.assigneeId } });
        setCurrentWorkspace(choice.workspaceSlug, choice.workspaceId);
        onOpenCenter();
      }
      setTitle("");
      setDescription("");
      setShowIssueForm(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally { setBusy(false); }
  }

  const selectedIssue = issues.find(issue => issue.id === selectedIssueId);

  return <div className={embedded ? "flex min-h-0 flex-1 flex-col bg-background text-foreground" : "flex h-screen flex-col bg-background text-foreground"}>
    {!embedded && <DragStrip />}
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-5 overflow-y-auto px-6 py-8">
      <div className="flex items-start justify-between gap-3">
        <div><h1 className="text-title font-semibold">Local issues</h1>
          <p className="text-body text-muted-foreground">Work on this machine without a Center connection.</p></div>
        <div className="flex gap-2">
          {onBack && <Button variant="outline" onClick={onBack}>Choose mode</Button>}
          {centerAvailable && <Button variant="outline" onClick={onOpenCenter}>Open Center</Button>}
          <Button disabled={!daemonRunning && !centerAvailable} onClick={() => setShowIssueForm(value => !value)} aria-expanded={showIssueForm}>
            {showIssueForm ? "Cancel" : "New issue"}
          </Button>
        </div>
      </div>
      {!embedded && <div className="rounded-lg border bg-card p-4"><LocalDaemonConnection /></div>}
      {!daemonRunning && <p role="status" className="text-body text-muted-foreground">
        Start the local daemon with <code>./run_multica.sh</code>. This page will reconnect automatically.
      </p>}
      <section className="space-y-2" aria-label="Local issues"><h2 className="font-semibold">Issues on this machine</h2>
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
          <div className="space-y-2">
            {issues.length === 0 && <p className="text-muted-foreground">No local issues yet.</p>}
            {issues.map(issue => <article key={issue.id} className="rounded-lg border bg-card p-4">
              <button type="button" className="flex w-full justify-between gap-2 text-left" aria-pressed={selectedIssueId === issue.id}
                onClick={() => setSelectedIssueId(issue.id)}><span className="font-medium">{issue.title}</span><span className="text-caption">{issue.status}</span></button>
              <p className="text-caption text-muted-foreground">Machine: Local · {issue.directory} · {issue.provider}</p>
              {issue.output && <pre className="mt-3 max-h-48 overflow-auto whitespace-pre-wrap text-caption">{issue.output}</pre>}
              {issue.error && <p className="mt-2 text-caption text-destructive">{issue.error}</p>}
            </article>)}
          </div>
          {selectedIssue && <aside className="self-start rounded-lg border bg-card p-4" aria-label="Issue properties">
            <h3 className="font-semibold">Properties</h3>
            <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-2 text-caption">
              <dt className="text-muted-foreground">Machine</dt><dd>Local (this machine)</dd>
              <dt className="text-muted-foreground">Status</dt><dd>{selectedIssue.status}</dd>
              <dt className="text-muted-foreground">Agent</dt><dd>{selectedIssue.provider}</dd>
              <dt className="text-muted-foreground">Directory</dt><dd className="break-all">{selectedIssue.directory}</dd>
            </dl>
          </aside>}
        </div>
      </section>
      {showIssueForm && <section className="space-y-3 rounded-lg border bg-card p-5" aria-label="Create issue">
        <h2 className="font-semibold">New issue</h2>
        <Input aria-label="Issue title" placeholder="What needs to be done?" value={title} onChange={event => setTitle(event.target.value)} />
        <textarea aria-label="Issue description" className="min-h-24 w-full rounded-md border bg-background p-2" placeholder="Details (optional)" value={description} onChange={event => setDescription(event.target.value)} />
        <label className="flex items-center gap-2 text-caption"><Switch checked={remoteOnly} disabled={busy} onCheckedChange={value => { setRemoteOnly(value); setMachine(""); setError(""); }} />Remote only</label>
        {remoteOnly && <label className="block space-y-1"><span className="text-caption">Remote agent or squad</span>
          <select aria-label="Issue machine" disabled={busy || !remoteReady} className="w-full rounded-md border bg-background p-2" value={machine} onChange={event => setMachine(event.target.value)}>
            <option value="">Select a remote target</option>
            {(["agent", "squad"] as const).map(type => {
              const choices = remoteChoices.filter(choice => choice.assigneeType === type)
                .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: "base" }) || a.id.localeCompare(b.id));
              if (choices.length === 0) return null;
              return <optgroup key={type} label={type === "agent" ? "Agents" : "Squads"}>
                {choices.map(choice => <option key={choice.id} value={choice.id}>{choice.label} · {type === "squad" ? "Squad" : "Agent"}</option>)}
              </optgroup>;
            })}
          </select>
        </label>}
        {!remoteOnly ? <>
          <div className="flex gap-2"><Input aria-label="Local working directory" placeholder={defaultDirectory || "Choose a local project directory"} value={directory} onChange={event => setDirectory(event.target.value)} />
            <Button variant="outline" onClick={() => void window.desktopAPI.pickDirectory(directory || defaultDirectory || undefined).then(result => { if (result.ok && result.path) setDirectory(result.path); })}>Browse</Button></div>
          <label className="block space-y-1"><span className="text-caption">Local agent</span>
            <select aria-label="Local agent" className="w-full rounded-md border bg-background p-2" value={provider} onChange={event => setProvider(event.target.value)}>
              <option value="">Default {agents.includes("codex") ? "(codex)" : ""}</option>
              {agents.map(name => <option key={name} value={name}>{name}</option>)}
            </select>
          </label>
          {capabilities && <p className="text-caption text-muted-foreground" data-testid="local-capabilities">
            Detected local skills: {capabilities.skills.map(skill => skill.name).join(", ") || "none"}. Local MCP servers: {capabilities.mcp_servers.filter(server => server.enabled).map(server => server.name).join(", ") || "none"}.
            Project-specific settings may also be loaded by the agent.
          </p>}
        </> : <div className="text-caption text-muted-foreground">
          <p>This issue is sent only to the selected remote agent or squad. No local fallback. Squads start on their leader’s machine; delegation follows each member’s runtime binding.</p>
          {!centerAvailable || !centerUserId ? <p role="alert">Connect and sign in to Center first.</p>
            : !daemonId ? <p role="alert">Waiting for this machine’s daemon identity before selecting remote targets.</p>
              : remoteQuery.isError ? <p role="alert">Could not load remote targets. <button type="button" className="underline" onClick={() => void remoteQuery.refetch()}>Retry</button></p>
                : remoteQuery.isPending ? <p>Loading remote targets...</p>
                  : remoteChoices.length === 0 ? <p>No remote agents or squads are available.</p> : null}
        </div>}
        <Button disabled={busy || !title.trim() || (remoteOnly ? !remoteReady || !remoteChoices.some(choice => choice.id === machine) : !daemonRunning || !(directory.trim() || defaultDirectory))} onClick={() => void createIssue()}>{busy ? "Creating…" : "Create issue"}</Button>
      </section>}
      {error && <p role="alert" className="text-destructive">{error}</p>}
      {!embedded && <div><Button variant="outline" onClick={() => setShowCenterSettings(value => !value)} aria-expanded={showCenterSettings}>Center settings</Button>
        {showCenterSettings && <div className="mt-3 rounded-lg border"><CenterSettingsTab /></div>}</div>}
    </main>
  </div>;
}

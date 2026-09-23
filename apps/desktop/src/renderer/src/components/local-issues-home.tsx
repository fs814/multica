import { useEffect, useState } from "react";
import { api } from "@multica/core/api";
import { setCurrentWorkspace } from "@multica/core/platform";
import { DragStrip } from "@multica/views/platform";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import type { LocalCapabilities, LocalIssue } from "../../../shared/local-issue";
import { CenterSettingsTab, LocalDaemonConnection } from "./center-settings-tab";

interface RemoteChoice {
  id: string;
  label: string;
  workspaceId: string;
  workspaceSlug: string;
  agentId: string;
}

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
  const [remoteChoices, setRemoteChoices] = useState<RemoteChoice[]>([]);
  const [machine, setMachine] = useState("local");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [directory, setDirectory] = useState("");
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

  useEffect(() => {
    let live = true;
    const refresh = async () => {
      try {
        const status = await window.daemonAPI.getStatus();
        if (!live) return;
        setDaemonRunning(status.state === "running");
        setAgents(status.agents ?? []);
        setDaemonId(status.daemonId);
        if (status.state !== "running") {
          setIssues([]);
          setError("");
          return;
        }
        const next = await window.daemonAPI.listLocalIssues();
        if (live) {
          setIssues(next);
          setSelectedIssueId(current => current && next.some(issue => issue.id === current) ? current : next[0]?.id ?? null);
          setError("");
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

  useEffect(() => {
    if (!centerAvailable || !centerUserId) { setRemoteChoices([]); return; }
    let live = true;
    void (async () => {
      try {
        const workspaces = await api.listWorkspaces();
        const choices: RemoteChoice[] = [];
        for (const workspace of workspaces) {
          const [runtimes, workspaceAgents] = await Promise.all([
            api.listRuntimes({ workspace_id: workspace.id }, workspace.slug),
            api.listAgents({ workspace_id: workspace.id }, workspace.slug),
          ]);
          for (const runtime of runtimes) {
            if (!runtime.daemon_id || runtime.daemon_id === daemonId || runtime.status !== "online") continue;
            for (const agent of workspaceAgents) {
              if (agent.runtime_id !== runtime.id) continue;
              choices.push({ id: `${workspace.id}:${runtime.id}:${agent.id}`,
                label: `${workspace.name} · ${runtime.custom_name || runtime.name} · ${agent.name}`,
                workspaceId: workspace.id, workspaceSlug: workspace.slug, agentId: agent.id });
            }
          }
        }
        if (live) setRemoteChoices(choices);
      } catch {
        // Remote discovery is optional; Center being offline must not prevent
        // an issue from running on the local daemon.
        if (live) setRemoteChoices([]);
      }
    })();
    return () => { live = false; };
  }, [centerAvailable, centerUserId, daemonId]);

  async function createIssue() {
    setError("");
    setBusy(true);
    try {
      if (machine === "local") {
        const issue = await window.daemonAPI.createLocalIssue({ title, description, directory, provider, machine: "local" });
        setIssues(current => [issue, ...current]);
        setSelectedIssueId(issue.id);
      } else {
        const choice = remoteChoices.find(item => item.id === machine);
        if (!choice) throw new Error("Selected remote machine is no longer available");
        // Center's existing agent-to-runtime binding is authoritative for
        // machine routing. No local issue data is sent for the default path.
        setCurrentWorkspace(choice.workspaceSlug, choice.workspaceId);
        await api.createIssue({ title, description, status: "todo", assignee_type: "agent", assignee_id: choice.agentId });
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
          <Button disabled={!daemonRunning} onClick={() => setShowIssueForm(value => !value)} aria-expanded={showIssueForm}>
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
        <label className="block space-y-1"><span className="text-caption">Machine</span>
          <select aria-label="Issue machine" className="w-full rounded-md border bg-background p-2" value={machine} onChange={event => setMachine(event.target.value)}>
            <option value="local">This machine (local)</option>
            {remoteChoices.map(choice => <option key={choice.id} value={choice.id}>{choice.label}</option>)}
          </select>
        </label>
        {machine === "local" ? <>
          <div className="flex gap-2"><Input aria-label="Local working directory" placeholder="Choose a local project directory" value={directory} onChange={event => setDirectory(event.target.value)} />
            <Button variant="outline" onClick={() => void window.desktopAPI.pickDirectory(directory || undefined).then(result => { if (result.ok && result.path) setDirectory(result.path); })}>Browse</Button></div>
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
        </> : <p className="text-caption text-muted-foreground">This issue is sent to Center and assigned to the selected machine’s agent.</p>}
        <Button disabled={busy || !title.trim() || (machine === "local" && !directory)} onClick={() => void createIssue()}>{busy ? "Creating…" : "Create issue"}</Button>
      </section>}
      {error && <p role="alert" className="text-destructive">{error}</p>}
      {!embedded && <div><Button variant="outline" onClick={() => setShowCenterSettings(value => !value)} aria-expanded={showCenterSettings}>Center settings</Button>
        {showCenterSettings && <div className="mt-3 rounded-lg border"><CenterSettingsTab /></div>}</div>}
    </main>
  </div>;
}

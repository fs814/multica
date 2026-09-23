import { useEffect, useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { DragStrip } from "@multica/views/platform";
import type { DaemonStatus } from "../../../shared/daemon-types";
import type { LocalCapabilities } from "../../../shared/local-issue";
import { CenterSettingsTab, LocalDaemonConnection } from "./center-settings-tab";
import { LocalIssuesHome } from "./local-issues-home";

type LocalPage = "issues" | "capabilities" | "settings";

/** Local-only workspace chrome; no Center API or authentication provider. */
export function LocalDaemonMode({ onBack, onOpenCenter, centerConfigured, centerUserId }: {
  onBack: () => void;
  onOpenCenter: () => void;
  centerConfigured: boolean;
  centerUserId: string | null;
}) {
  const [page, setPage] = useState<LocalPage>("issues");
  const [status, setStatus] = useState<DaemonStatus>({ state: "stopped" });
  const [provider, setProvider] = useState("");
  const [capabilities, setCapabilities] = useState<LocalCapabilities | null>(null);

  useEffect(() => {
    let live = true;
    const refresh = () => void window.daemonAPI.getStatus().then(value => {
      if (live) setStatus(value);
    }).catch(() => { if (live) setStatus({ state: "stopped" }); });
    refresh();
    const timer = setInterval(refresh, 3000);
    return () => { live = false; clearInterval(timer); };
  }, []);

  const agents = status.agents ?? [];
  const selectedProvider = provider && agents.includes(provider) ? provider : agents.includes("codex") ? "codex" : agents[0] ?? "";
  useEffect(() => {
    if (page !== "capabilities" || status.state !== "running" || !selectedProvider) {
      setCapabilities(null);
      return;
    }
    let live = true;
    void window.daemonAPI.getLocalCapabilities(selectedProvider).then(value => {
      if (live) setCapabilities(value);
    }).catch(() => { if (live) setCapabilities(null); });
    return () => { live = false; };
  }, [page, selectedProvider, status.state]);

  const nav: { id: LocalPage; label: string }[] = [
    { id: "issues", label: "Issues" },
    { id: "capabilities", label: "Skills & MCP" },
    { id: "settings", label: "Settings" },
  ];

  return <div className="flex h-screen flex-col bg-app-shell text-foreground">
    <DragStrip />
    <div className="flex min-h-0 flex-1">
      <aside className="flex w-56 shrink-0 flex-col gap-5 border-r px-3 py-5" aria-label="Local workspace navigation">
        <div className="px-3"><div className="font-semibold">Multica</div><div className="text-caption text-muted-foreground">This machine</div></div>
        <nav className="space-y-1" aria-label="Local workspace">
          {nav.map(item => <button key={item.id} type="button" aria-current={page === item.id ? "page" : undefined}
            className={`w-full rounded-lg px-3 py-2 text-left text-body ${page === item.id ? "bg-muted font-medium" : "hover:bg-muted/60"}`}
            onClick={() => setPage(item.id)}>{item.label}</button>)}
        </nav>
        <div className="mt-auto space-y-3 px-3 text-caption"><LocalDaemonConnection />
          <Button variant="outline" className="w-full" onClick={onBack}>Choose mode</Button>
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col p-2">
        <header className="flex min-h-12 items-center justify-between px-4"><span className="text-caption text-muted-foreground">Local daemon workspace</span>
          {centerConfigured && <Button variant="outline" onClick={onOpenCenter}>Open Center</Button>}
        </header>
        <main className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-xl border bg-background" aria-label="Local workspace content">
          {page === "issues" ? <LocalIssuesHome embedded centerAvailable={centerConfigured} centerUserId={centerUserId} onOpenCenter={onOpenCenter} /> :
            page === "settings" ? <div className="overflow-y-auto"><CenterSettingsTab /></div> :
              <div className="space-y-5 overflow-y-auto p-6">
                <div><h1 className="text-title font-semibold">Local skills & MCP</h1>
                  <p className="text-body text-muted-foreground">Discovered on this machine. Credentials and configuration remain local.</p></div>
                {status.state !== "running" ? <p role="status">Start the local daemon to inspect its capabilities.</p> : agents.length === 0 ?
                  <p role="status">No local agent CLI was detected.</p> : <>
                    <label className="block space-y-1"><span className="text-caption">Agent</span>
                      <select aria-label="Capability agent" className="w-full max-w-xs rounded-md border bg-background p-2" value={selectedProvider} onChange={event => setProvider(event.target.value)}>
                        {agents.map(name => <option key={name} value={name}>{name}</option>)}
                      </select>
                    </label>
                    {!capabilities ? <p role="status">Loading local capabilities…</p> : <div className="grid gap-4 sm:grid-cols-2">
                      <section className="rounded-lg border bg-card p-4" aria-label="Local skills"><h2 className="font-semibold">Skills</h2>
                        {capabilities.skills.length === 0 ? <p className="mt-2 text-muted-foreground">No skills found.</p> :
                          <ul className="mt-2 space-y-1">{capabilities.skills.map(skill => <li key={skill.key}>{skill.name}</li>)}</ul>}
                      </section>
                      <section className="rounded-lg border bg-card p-4" aria-label="Local MCP servers"><h2 className="font-semibold">MCP servers</h2>
                        {capabilities.mcp_servers.length === 0 ? <p className="mt-2 text-muted-foreground">No MCP servers found.</p> :
                          <ul className="mt-2 space-y-1">{capabilities.mcp_servers.map(server => <li key={server.name}>{server.name}{server.enabled ? "" : " (disabled)"}</li>)}</ul>}
                      </section>
                    </div>}
                  </>}
              </div>}
        </main>
      </div>
    </div>
  </div>;
}

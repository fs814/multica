import { useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { DragStrip } from "@multica/views/platform";
import { CenterSettingsTab, LocalDaemonConnection } from "./center-settings-tab";

/** Choosing local mode must not mount CoreProvider or contact Center. */
export function DesktopModePicker({ centerConfigured, centerError, onLocal, onCenter }: {
  centerConfigured: boolean;
  centerError?: string;
  onLocal: () => void;
  onCenter: () => void;
}) {
  const [showCenterSettings, setShowCenterSettings] = useState(false);

  return <div className="flex h-screen flex-col bg-background text-foreground">
    <DragStrip />
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col justify-center gap-6 overflow-y-auto px-6 py-8">
      <div>
        <h1 className="text-title font-semibold">Multica Desktop</h1>
        <p className="mt-2 text-body text-muted-foreground">Choose where to work. Local mode does not require a Center server or sign-in.</p>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <section className="flex flex-col gap-4 rounded-xl border bg-card p-5" aria-label="Local daemon mode">
          <div><h2 className="font-semibold">Local daemon</h2>
            <p className="mt-2 text-body text-muted-foreground">Run local issues with this machine’s agents, skills, and MCP servers.</p></div>
          <div className="mt-auto"><LocalDaemonConnection /></div>
          <Button onClick={onLocal}>Enter local mode</Button>
        </section>
        <section className="flex flex-col gap-4 rounded-xl border bg-card p-5" aria-label="Center mode">
          <div><h2 className="font-semibold">Center</h2>
            <p className="mt-2 text-body text-muted-foreground">Open the regular Multica workspace and work with other machines.</p></div>
          <p className="mt-auto text-caption text-muted-foreground">{centerConfigured ? "Center address saved" : "Center address not configured"}</p>
          <Button variant="outline" onClick={() => centerConfigured ? onCenter() : setShowCenterSettings(true)}>
            {centerConfigured ? "Open Multica workspace" : "Set up Center"}
          </Button>
        </section>
      </div>
      {centerError && <p role="alert" className="text-caption text-destructive">{centerError}</p>}
      {showCenterSettings && <div className="rounded-xl border bg-card"><CenterSettingsTab /></div>}
    </main>
  </div>;
}

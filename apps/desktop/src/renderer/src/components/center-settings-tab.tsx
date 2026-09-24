import { useEffect, useState } from "react";
import { useT } from "@multica/views/i18n";
import { useWS } from "@multica/core/realtime";
import { useAuthStore } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import type { CenterSettingsState } from "../../../shared/center-settings";
import type { DaemonStatus } from "../../../shared/daemon-types";

export function CenterSettingsTab() {
  const { t } = useT("settings");
  const [settings, setSettings] = useState<CenterSettingsState | null>(null);
  const [url, setUrl] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  useEffect(() => { void window.desktopAPI.center.get().then(value => {
    setSettings(value); setUrl(value.saved?.url ?? ''); setMessage(value.error ?? '');
  }).catch(error => setMessage(String(error))); }, []);
  async function run(action: () => Promise<void>) {
    setBusy(true); setMessage('');
    try { await action(); } catch (error) { setMessage(error instanceof Error ? error.message : String(error)); }
    finally { setBusy(false); }
  }
  return <section className="space-y-5 p-6" aria-label={t(($) => $.desktop.center.settings)}>
    <div><h2 className="text-title font-semibold">{t(($) => $.desktop.center.title)}</h2>
      <p className="text-body text-muted-foreground">{t(($) => $.desktop.center.description)}</p></div>
    <label className="block space-y-2"><span>{t(($) => $.desktop.center.address)}</span>
      <Input aria-label={t(($) => $.desktop.center.address)} value={url} placeholder="http://192.168.1.20:18080" onChange={event => { setUrl(event.target.value); setMessage(''); }} />
    </label>
    <div className="flex flex-wrap gap-2">
      <Button disabled={busy || !url.trim()} onClick={() => void run(async () => {
        setSettings(await window.desktopAPI.center.save(url));
        setMessage(t(($) => $.desktop.center.saved_message));
      })}>{t(($) => $.desktop.center.save)}</Button>
      <Button variant="outline" disabled={busy || !url.trim()} onClick={() => void run(async () => {
        const result = await window.desktopAPI.center.test(url);
        setMessage(result.reachable ? t(($) => $.desktop.center.reachable) : t(($) => $.desktop.center.failed));
      })}>{t(($) => $.desktop.center.test)}</Button>
      <Button variant="outline" disabled={busy || !settings?.saved} onClick={() => void run(async () => {
        await window.desktopAPI.center.connect();
      })}>{t(($) => $.desktop.center.connect)}</Button>
    </div>
    <p className="break-all text-caption text-muted-foreground">{t(($) => $.desktop.center.saved)}: {settings?.saved?.url ?? t(($) => $.desktop.center.not_configured)}<br />{t(($) => $.desktop.center.active)}: {settings?.activeUrl ?? t(($) => $.desktop.center.none)}</p>
    <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.hint)}</p>
    <p role="status" className="text-body">{message}</p>
  </section>;
}

export function CenterConnectionPanel() {
  const { t } = useT("settings");
  const { connected } = useWS();
  const user = useAuthStore(state => state.user);
  const [daemon, setDaemon] = useState<DaemonStatus>({ state: 'stopped' });
  const [open, setOpen] = useState(false);
  useEffect(() => {
    void window.daemonAPI.getStatus().then(setDaemon);
    return window.daemonAPI.onStatusChange(setDaemon);
  }, []);
  const active = window.desktopAPI.runtimeConfig;
  const daemonConnected = daemon.centerConnected === true && active.ok && daemon.serverUrl === active.config.apiUrl;
  return <div className="fixed bottom-20 right-4 z-50 max-w-lg rounded-lg border bg-background shadow-md" style={{ WebkitAppRegion: 'no-drag' } as React.CSSProperties}>
    <button className="p-3 text-caption text-left" onClick={() => setOpen(!open)} aria-expanded={open}>
      {t(($) => $.desktop.center.center)} · Desktop: {connected ? t(($) => $.desktop.center.connected) : user ? t(($) => $.desktop.center.reconnecting) : t(($) => $.desktop.center.sign_in)} · daemon: {daemonConnected ? t(($) => $.desktop.center.connected) : daemon.state === 'running' && daemon.centerConnected === undefined ? t(($) => $.desktop.center.unknown) : daemon.state === 'auth_expired' ? t(($) => $.desktop.center.sign_in) : t(($) => $.desktop.center.disconnected)}
    </button>
    {open && <div className="max-h-[75vh] overflow-y-auto"><CenterSettingsTab /></div>}
  </div>;
}

// Available before authentication and even if a saved configuration is invalid.
export function CenterSettingsAccess() {
  const { t } = useT("settings");
  const [open, setOpen] = useState(false);
  return <div className="fixed bottom-20 right-4 z-50 max-w-lg rounded-lg border bg-background shadow-md" style={{ WebkitAppRegion: 'no-drag' } as React.CSSProperties}>
    <button className="p-3 text-caption" onClick={() => setOpen(!open)}>{t(($) => $.desktop.center.settings)}</button>
    {open && <CenterSettingsTab />}
  </div>;
}

import { useId, useState } from "react";
import { useT } from "@multica/views/i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { normalizeCenterUrl, type CenterSettingsState } from "../../../shared/center-settings";
import { CenterSyncConnect } from "./center-sync-connect";

interface Props {
  settings: CenterSettingsState | null;
  disabled: boolean;
  onBusyChange: (busy: boolean) => void;
  onSaved: (settings: CenterSettingsState) => void;
}

export function CenterSyncSettings({ settings, disabled, onBusyChange, onSaved }: Props) {
  const { t } = useT("settings");
  const scopeId = useId();
  const [draftUrl, setDraftUrl] = useState<string | null>(null);
  // Retain the existing address-only persistence contract; it does not enroll a peer.
  const url = draftUrl ?? settings?.transferUrl ?? "";
  const [message, setMessage] = useState("");
  let peer = "";
  try { peer = normalizeCenterUrl(url); } catch { /* Keep invalid drafts editable. */ }
  const canConnect = !!peer && peer === settings?.transferUrl && peer !== settings?.activeUrl;

  async function run(action: () => Promise<void>) {
    onBusyChange(true);
    setMessage("");
    try { await action(); }
    catch (error) { setMessage(error instanceof Error ? error.message : String(error)); }
    finally { onBusyChange(false); }
  }

  return <section className="space-y-3" aria-label={t(($) => $.desktop.center.sync_title)}>
    <h3 className="text-title font-semibold">{t(($) => $.desktop.center.sync_title)}</h3>
    <label className="block space-y-2"><span>{t(($) => $.desktop.center.sync_address)}</span>
      <Input value={url} disabled={disabled} placeholder="https://center.example.com" onChange={event => { setDraftUrl(event.target.value); setMessage(""); }} />
    </label>
    <div className="flex flex-wrap gap-2">
      <Button variant="outline" disabled={disabled || !url.trim()} onClick={() => void run(async () => {
        const saved = await window.desktopAPI.center.saveTransfer(url);
        onSaved(saved);
        setDraftUrl(null);
        setMessage(t(($) => $.desktop.center.sync_saved));
      })}>{t(($) => $.desktop.center.save)}</Button>
      <Button variant="outline" disabled={disabled || !url.trim()} onClick={() => void run(async () => {
        const result = await window.desktopAPI.center.test(url);
        setMessage(result.reachable ? t(($) => $.desktop.center.sync_reachable) : t(($) => $.desktop.center.failed));
      })}>{t(($) => $.desktop.center.test)}</Button>
      {canConnect
        ? <CenterSyncConnect key={`${settings?.activeUrl}:${peer}`} address={peer} sourceAddress={settings?.activeUrl ?? ""} disabled={disabled} onBusyChange={onBusyChange} />
        : <>
          <Button variant="outline" disabled>{t(($) => $.desktop.center.sync_connect)}</Button>
          <Button variant="outline" disabled>{t(($) => $.desktop.center.sync_disconnect)}</Button>
          <Button disabled aria-describedby={scopeId}>{t(($) => $.desktop.center.sync_data)}</Button>
        </>}
    </div>
    {!canConnect && !!url.trim() && <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_connect_hint)}</p>}
    <p id={scopeId} className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_scope)}</p>
    {settings?.transferError && <p role="alert" className="text-body text-destructive">{settings.transferError}</p>}
    <p role="status" className="text-body">{message}</p>
  </section>;
}

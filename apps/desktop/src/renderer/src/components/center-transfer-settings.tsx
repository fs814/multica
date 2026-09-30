import { useState } from "react";
import { useT } from "@multica/views/i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, DialogClose } from "@multica/ui/components/ui/dialog";
import { normalizeCenterUrl, type CenterSettingsState } from "../../../shared/center-settings";

interface Props {
  settings: CenterSettingsState | null;
  disabled: boolean;
  onBusyChange: (busy: boolean) => void;
  onSaved: (settings: CenterSettingsState) => void;
}

export function CenterTransferSettings({ settings, disabled, onBusyChange, onSaved }: Props) {
  const { t } = useT("settings");
  const [draftUrl, setDraftUrl] = useState<string | null>(null);
  const url = draftUrl ?? settings?.transferUrl ?? "";
  const [message, setMessage] = useState("");
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [sourceToken, setSourceToken] = useState("");
  const [targetToken, setTargetToken] = useState("");
  const [transferMessage, setTransferMessage] = useState("");
  let normalizedUrl = "";
  try { normalizedUrl = normalizeCenterUrl(url); } catch { /* Keep an invalid draft editable. */ }
  const canTransfer = !!settings?.activeUrl && !!normalizedUrl && normalizedUrl === settings?.transferUrl && normalizedUrl !== settings.activeUrl;

  async function run(action: () => Promise<void>) {
    onBusyChange(true); setMessage("");
    try { await action(); }
    catch (error) { setMessage(error instanceof Error ? error.message : String(error)); }
    finally { onBusyChange(false); }
  }

  async function transfer() {
    if (!canTransfer || disabled || pending) return;
    setPending(true); onBusyChange(true); setMessage("");
    setTransferMessage(t(($) => $.desktop.center.transfer_progress));
    try {
      const result = await window.desktopAPI.center.transferData({ targetUrl: normalizedUrl, sourceRecoveryToken: sourceToken, targetRecoveryToken: targetToken });
      if (result.cancelled) { setTransferMessage(""); return; }
      setMessage(t(($) => $.desktop.center.transfer_success, { address: normalizedUrl }));
      setOpen(false); setSourceToken(""); setTargetToken(""); setTransferMessage("");
    } catch (error) { setTransferMessage(error instanceof Error ? error.message : String(error)); }
    finally { setPending(false); onBusyChange(false); }
  }

  return <section className="space-y-3" aria-label={t(($) => $.desktop.center.transfer_title)}>
    <h3 className="text-title font-semibold">{t(($) => $.desktop.center.transfer_title)}</h3>
    <label className="block space-y-2"><span>{t(($) => $.desktop.center.transfer_address)}</span>
      <Input value={url} disabled={disabled || pending} placeholder="http://192.168.1.30:18080" onChange={event => { setDraftUrl(event.target.value); setMessage(""); }} />
    </label>
    <div className="flex flex-wrap gap-2">
      <Button variant="outline" disabled={disabled || pending || !url.trim()} onClick={() => void run(async () => {
        const saved = await window.desktopAPI.center.saveTransfer(url);
        onSaved(saved); setDraftUrl(null);
        setMessage(t(($) => $.desktop.center.transfer_saved));
      })}>{t(($) => $.desktop.center.save)}</Button>
      <Button variant="outline" disabled={disabled || pending || !url.trim()} onClick={() => void run(async () => {
        const result = await window.desktopAPI.center.test(url);
        setMessage(result.reachable ? t(($) => $.desktop.center.reachable) : t(($) => $.desktop.center.failed));
      })}>{t(($) => $.desktop.center.test)}</Button>
      <Dialog open={open} onOpenChange={value => {
        if (pending) return;
        setOpen(value); setTransferMessage("");
        if (!value) { setSourceToken(""); setTargetToken(""); }
      }}>
        <DialogTrigger render={<Button className="h-auto min-h-[var(--button-height-default)] max-w-full whitespace-normal" disabled={disabled || pending || !canTransfer} />}>{t(($) => $.desktop.center.transfer_data)}</DialogTrigger>
        <DialogContent showCloseButton={!pending}>
          <DialogHeader>
            <DialogTitle>{t(($) => $.desktop.center.transfer_data)}</DialogTitle>
            <DialogDescription>{t(($) => $.desktop.center.transfer_description)}</DialogDescription>
          </DialogHeader>
          <p className="break-all text-body">{settings?.activeUrl} → {normalizedUrl}</p>
          <form onSubmit={event => { event.preventDefault(); void transfer(); }} className="space-y-4">
            <label className="block space-y-2"><span>{t(($) => $.desktop.center.recovery_token)}</span>
              <Input type="password" autoComplete="off" maxLength={4096} disabled={pending} value={sourceToken} onChange={event => setSourceToken(event.target.value)} />
            </label>
            <label className="block space-y-2"><span>{t(($) => $.desktop.center.transfer_token)}</span>
              <Input type="password" autoComplete="off" maxLength={4096} disabled={pending} value={targetToken} onChange={event => setTargetToken(event.target.value)} />
            </label>
            <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.transfer_token_hint)}</p>
            <p role="status" className="text-body">{transferMessage}</p>
            <DialogFooter>
              <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>{t(($) => $.desktop.center.recovery_cancel)}</DialogClose>
              <Button className="h-auto min-h-[var(--button-height-default)] min-w-0 shrink whitespace-normal" type="submit" disabled={disabled || pending || !canTransfer} aria-busy={pending}>{pending ? t(($) => $.desktop.center.recovery_working) : t(($) => $.desktop.center.transfer_data)}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
    {!!url.trim() && !canTransfer && <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.transfer_save_hint)}</p>}
    {settings?.transferError && <p role="alert" className="text-body text-destructive">{settings.transferError}</p>}
    <p role="status" className="text-body">{message}</p>
  </section>;
}

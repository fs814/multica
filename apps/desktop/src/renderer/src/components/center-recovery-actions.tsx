import { useState } from "react";
import { useT } from "@multica/views/i18n";
import { useAuthStore } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, DialogClose } from "@multica/ui/components/ui/dialog";

interface Props { disabled: boolean; onBusyChange: (busy: boolean) => void; onMessage: (message: string) => void }
function RecoveryAction({ mode, disabled, onBusyChange, onMessage }: Props & { mode: "export" | "import" }) {
  const { t } = useT("settings");
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [recoveryToken, setRecoveryToken] = useState("");
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  const label = mode === "export" ? t(($) => $.desktop.center.export_data) : t(($) => $.desktop.center.import_data);
  async function submit() {
    setPending(true); onBusyChange(true); setMessage(""); onMessage("");
    try {
      const request = { password, recoveryToken };
      const result = mode === "export" ? await window.desktopAPI.center.exportData(request) : await window.desktopAPI.center.importData(request);
      if (result.cancelled) return;
      if (mode === "import") {
        if (!result.jobId) throw new Error(t(($) => $.desktop.center.import_failed));
        setMessage(t(($) => $.desktop.center.import_progress));
        const deadline = Date.now() + 15 * 60 * 1000;
        for (;;) {
          const status = await window.desktopAPI.center.importStatus(result.jobId);
          if (status.state === "failed") throw new Error(status.message || t(($) => $.desktop.center.import_failed));
          if (status.state === "complete") break;
          if (Date.now() > deadline) throw new Error(t(($) => $.desktop.center.import_timeout));
          await new Promise(resolve => setTimeout(resolve, 2000));
        }
        useAuthStore.getState().logout();
        onMessage(t(($) => $.desktop.center.import_success));
      } else onMessage(t(($) => $.desktop.center.export_success));
      setOpen(false); setPassword(""); setConfirmation(""); setRecoveryToken("");
    } catch (error) { setMessage(error instanceof Error ? error.message : String(error)); }
    finally { setPending(false); onBusyChange(false); }
  }
  return <Dialog open={open} onOpenChange={value => { if (!pending) { setOpen(value); setMessage(""); if (!value) { setPassword(""); setConfirmation(""); setRecoveryToken(""); } } }}>
    <DialogTrigger render={<Button variant="outline" disabled={disabled} />}>{label}</DialogTrigger>
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{label}</DialogTitle>
        <DialogDescription>{mode === "export" ? t(($) => $.desktop.center.export_description) : t(($) => $.desktop.center.import_description)}</DialogDescription>
      </DialogHeader>
      <form onSubmit={event => { event.preventDefault(); void submit(); }} className="space-y-4">
        <label className="block space-y-2"><span>{t(($) => $.desktop.center.backup_password)}</span>
          <Input type="password" autoComplete="new-password" minLength={12} maxLength={1024} required value={password} disabled={pending} onChange={event => setPassword(event.target.value)} />
        </label>
        {mode === "export" && <label className="block space-y-2"><span>{t(($) => $.desktop.center.confirm_password)}</span>
          <Input type="password" autoComplete="new-password" required value={confirmation} disabled={pending} onChange={event => setConfirmation(event.target.value)} />
        </label>}
        <label className="block space-y-2"><span>{t(($) => $.desktop.center.recovery_token)}</span>
          <Input type="password" autoComplete="off" maxLength={4096} value={recoveryToken} disabled={pending} onChange={event => setRecoveryToken(event.target.value)} />
          <span className="block text-caption text-muted-foreground">{t(($) => $.desktop.center.recovery_token_hint)}</span>
        </label>
        <p role="status" className="text-body">{message}</p>
        <DialogFooter>
          <DialogClose render={<Button type="button" variant="outline" disabled={pending} />}>{t(($) => $.desktop.center.recovery_cancel)}</DialogClose>
          <Button type="submit" disabled={pending || password.length < 12 || (mode === "export" && password !== confirmation)} aria-busy={pending}>{pending ? t(($) => $.desktop.center.recovery_working) : label}</Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>;
}
export function CenterRecoveryActions(props: Props) {
  return <div className="flex flex-wrap gap-2"><RecoveryAction {...props} mode="export" /><RecoveryAction {...props} mode="import" /></div>;
}

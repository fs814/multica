import { useContext, useEffect, useRef, useState } from "react";
import { QueryClientContext } from "@tanstack/react-query";
import { CenterSyncSession, type CenterSyncUser } from "@multica/core/api/center-sync-session";
import { useT } from "@multica/views/i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, DialogClose } from "@multica/ui/components/ui/dialog";
import { CenterSyncRun } from "./center-sync-run";

interface Props {
  address: string;
  disabled: boolean;
  sourceAddress: string;
  onBusyChange: (busy: boolean) => void;
}

// The parent keys this component by source + destination. Changing either
// tears down the destination session and aborts any outstanding login request.
export function CenterSyncConnect({ address, disabled, sourceAddress, onBusyChange }: Props) {
  const { t } = useT("settings");
  const queryClient = useContext(QueryClientContext);
  const [session] = useState(() => new CenterSyncSession(address));
  const [user, setUser] = useState<CenterSyncUser | null>(null);
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [sent, setSent] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef(0);
  useEffect(() => () => { attempt.current++; session.disconnect(); }, [session]);

  function close() {
    attempt.current++;
    session.disconnect();
    setOpen(false); setPending(false); setCode(""); setSent(false); setError("");
  }

  async function submit() {
    if (pending || disabled) return;
    const current = ++attempt.current;
    setPending(true); setError("");
    try {
      if (!sent) {
        await session.sendCode(email.trim());
        if (current === attempt.current) setSent(true);
      } else {
        const connected = await session.verifyCode(email.trim(), code.trim());
        if (current !== attempt.current) return;
        setUser(connected); setOpen(false); setCode(""); setSent(false);
      }
    } catch {
      if (current === attempt.current) setError(t(($) => $.desktop.center.sync_sign_in_error));
    } finally {
      if (current === attempt.current) setPending(false);
    }
  }

  return <div className="w-full max-w-full space-y-2">
    <div className="flex flex-wrap gap-2">
    <Dialog open={open} onOpenChange={value => { if (value) setOpen(true); else close(); }}>
    <DialogTrigger render={<Button variant="outline" disabled={disabled || !!user} />}>{t(($) => $.desktop.center.sync_connect)}</DialogTrigger>
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{t(($) => $.desktop.center.sync_connect)}</DialogTitle>
        <DialogDescription>{t(($) => $.desktop.center.sync_connect_description, { address: session.origin })}</DialogDescription>
      </DialogHeader>
      <form className="space-y-4" onSubmit={event => { event.preventDefault(); void submit(); }}>
        <label className="block space-y-2"><span>{t(($) => $.desktop.center.sync_email)}</span>
          <Input type="email" autoComplete="email" required disabled={pending || sent} value={email} onChange={event => setEmail(event.target.value)} />
        </label>
        {sent && <label className="block space-y-2"><span>{t(($) => $.desktop.center.sync_code)}</span>
          <Input autoComplete="one-time-code" inputMode="numeric" required maxLength={6} pattern="[0-9]{6}" disabled={pending} value={code} onChange={event => setCode(event.target.value)} />
        </label>}
        {session.origin.startsWith("http:") && <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_http_warning)}</p>}
        {error && <p role="alert" className="text-body text-destructive">{error}</p>}
        <DialogFooter>
          <DialogClose render={<Button type="button" variant="outline" />}>{t(($) => $.desktop.center.recovery_cancel)}</DialogClose>
          <Button type="submit" disabled={disabled || pending || !email.trim() || (sent && !/^\d{6}$/.test(code))} aria-busy={pending}>
            {sent ? t(($) => $.desktop.center.sync_sign_in) : t(($) => $.desktop.center.sync_send_code)}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
    </Dialog>
    <Button variant="outline" disabled={!user} onClick={() => {
      close(); setUser(null); setEmail("");
    }}>
      {t(($) => $.desktop.center.sync_disconnect)}
    </Button>
    </div>
    {user && <>
      <p role="status" className="break-all text-body">{t(($) => $.desktop.center.sync_connected, { address: session.origin, email: user.email })}</p>
      <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_connection_lifetime)}</p>
      {queryClient && sourceAddress
        ? <CenterSyncRun sourceAddress={sourceAddress} session={session} disabled={disabled} onBusyChange={onBusyChange} onExpired={() => setUser(null)} />
        : <><p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_source_required)}</p><Button disabled>{t(($) => $.desktop.center.sync_data)}</Button></>}
    </>}
    {!user && <Button disabled>{t(($) => $.desktop.center.sync_data)}</Button>}
  </div>;
}

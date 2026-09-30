import { useContext, useEffect, useRef, useState } from "react";
import { QueryClientContext } from "@tanstack/react-query";
import { CenterSyncRequestError, CenterSyncSession, type CenterSyncUser } from "@multica/core/api/center-sync-session";
import { useT } from "@multica/views/i18n";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogTrigger, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter, DialogClose } from "@multica/ui/components/ui/dialog";
import { CenterSyncRun } from "./center-sync-run";
import { createCenterSyncFetch } from "../platform/center-sync-fetch";

interface Props {
  address: string;
  disabled: boolean;
  sourceAddress: string;
  onBusyChange: (busy: boolean) => void;
  role?: "source" | "peer";
  sourceSession?: CenterSyncSession | null;
  onSessionChange?: (session: CenterSyncSession | null) => void;
  onSourceExpired?: () => void;
}

// The parent keys this component by source + destination. Changing either
// tears down the destination session and aborts any outstanding login request.
export function CenterSyncConnect({ address, disabled, sourceAddress, onBusyChange, role = "peer", sourceSession, onSessionChange, onSourceExpired }: Props) {
  const { t } = useT("settings");
  const queryClient = useContext(QueryClientContext);
  const [session] = useState(() => new CenterSyncSession(address,
    createCenterSyncFetch(new URL(address).origin, role === "source" ? {
      syncRequest: request => window.desktopAPI.center.syncSourceRequest(request),
      cancelSyncRequest: id => window.desktopAPI.center.cancelSyncSourceRequest(id),
    } : window.desktopAPI.center)));
  const [user, setUser] = useState<CenterSyncUser | null>(null);
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [sent, setSent] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef(0);
  useEffect(() => () => { attempt.current++; session.disconnect(); onSessionChange?.(null); }, [session, onSessionChange]);
  const connected = user && session.currentUser;
  const connectLabel = role === "source" ? t(($) => $.desktop.center.sync_source_connect) : t(($) => $.desktop.center.sync_connect);
  const disconnectLabel = role === "source" ? t(($) => $.desktop.center.sync_source_disconnect) : t(($) => $.desktop.center.sync_disconnect);

  function close() {
    attempt.current++;
    session.disconnect();
    onSessionChange?.(null);
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
        onSessionChange?.(session);
      }
    } catch (failure) {
      if (current === attempt.current) {
        let reason = t(($) => $.desktop.center.sync_sign_in_error);
        if (failure instanceof CenterSyncRequestError) {
          if (failure.reason === "network") reason = t(($) => $.desktop.center.sync_login_network);
          else if (failure.reason === "timeout") reason = t(($) => $.desktop.center.sync_login_timeout);
          else if (failure.reason === "invalid_response") reason = t(($) => $.desktop.center.sync_login_response);
          else if (failure.status === 429) reason = t(($) => $.desktop.center.sync_login_rate_limited);
          else if (failure.status === 403) reason = t(($) => $.desktop.center.sync_login_forbidden);
          else if (failure.status === 400 && sent) reason = t(($) => $.desktop.center.sync_login_code);
          else reason = t(($) => $.desktop.center.sync_login_http, { status: failure.status });
        }
        setError(t(($) => $.desktop.center.sync_login_failed, {
          step: sent ? t(($) => $.desktop.center.sync_sign_in) : t(($) => $.desktop.center.sync_send_code), reason,
        }));
      }
    } finally {
      if (current === attempt.current) setPending(false);
    }
  }

  return <div className="w-full max-w-full space-y-2">
    <div className="flex flex-wrap gap-2">
    <Dialog open={open} onOpenChange={value => { if (value) setOpen(true); else close(); }}>
    <DialogTrigger render={<Button variant="outline" disabled={disabled || !!connected} />}>{connectLabel}</DialogTrigger>
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{connectLabel}</DialogTitle>
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
    <Button variant="outline" disabled={!connected} onClick={() => {
      close(); setUser(null); setEmail("");
    }}>
      {disconnectLabel}
    </Button>
    </div>
    {connected && <>
      <p role="status" className="break-all text-body">{t(($) => $.desktop.center.sync_connected, { address: session.origin, email: connected.email })}</p>
      <p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_connection_lifetime)}</p>
      {role === "peer" && (queryClient && sourceAddress && sourceSession !== null && (!sourceSession || sourceSession.currentUser)
        ? <CenterSyncRun key={sourceSession ? `${sourceSession.origin}:${sourceSession.currentUser?.id}` : "primary"} sourceAddress={sourceAddress} sourceSession={sourceSession} session={session} disabled={disabled} onBusyChange={onBusyChange} onExpired={() => setUser(null)} onSourceExpired={onSourceExpired} />
        : <><p className="text-caption text-muted-foreground">{t(($) => $.desktop.center.sync_source_required)}</p><Button disabled>{t(($) => $.desktop.center.sync_data)}</Button></>)}
    </>}
    {!connected && role === "peer" && <Button disabled>{t(($) => $.desktop.center.sync_data)}</Button>}
  </div>;
}

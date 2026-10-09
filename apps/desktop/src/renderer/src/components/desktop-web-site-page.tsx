import { createElement, useEffect, useRef, useState } from "react";
import type { WebviewTag } from "electron";
import { ArrowLeft, ArrowRight, ExternalLink, Loader2, RotateCw } from "lucide-react";
import { useParams } from "react-router-dom";
import { normalizeWebUrl, useWebLinksStore, type WebLink } from "@multica/core/web-links";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "@multica/views/i18n";
import { openExternal, useRestoredViewState, useViewStateWriter } from "@multica/views/platform";

export function DesktopWebSitePage() {
  const { siteId } = useParams<{ siteId: string }>();
  const site = useWebLinksStore((state) => state.links.find((link) => link.id === siteId));
  const { t } = useT("web-links");
  if (!site) return <p className="p-6 text-body text-muted-foreground">{t(($) => $.site_missing)}</p>;
  return <EmbeddedWebSite key={site.id + site.url} site={site} />;
}

export function EmbeddedWebSite({ site }: { site: WebLink }) {
  const { t } = useT("web-links");
  const view = useRef<WebviewTag | null>(null);
  const restored = useRestoredViewState("web-site-url");
  const writeState = useViewStateWriter();
  const [initialUrl] = useState(() => normalizeWebUrl(restored) ?? site.url);
  const [url, setUrl] = useState(initialUrl);
  const [loading, setLoading] = useState(true);
  const [ready, setReady] = useState(false);
  const [failed, setFailed] = useState(false);
  const [canBack, setCanBack] = useState(false);
  const [canForward, setCanForward] = useState(false);

  useEffect(() => {
    const guest = view.current;
    if (!guest) return;
    const sync = () => {
      const next = normalizeWebUrl(guest.getURL());
      if (next) { setUrl(next); writeState("web-site-url", next); }
      setCanBack(guest.canGoBack());
      setCanForward(guest.canGoForward());
    };
    const loaded = () => { setReady(true); sync(); };
    const start = () => { setLoading(true); setFailed(false); };
    const stop = () => { setLoading(false); sync(); };
    const fail = (event: Event) => {
      const error = event as Event & { isMainFrame: boolean; errorCode: number };
      if (error.isMainFrame && error.errorCode !== -3) { setFailed(true); setLoading(false); setReady(true); }
    };
    guest.addEventListener("dom-ready", loaded);
    guest.addEventListener("did-start-loading", start);
    guest.addEventListener("did-stop-loading", stop);
    guest.addEventListener("did-navigate", sync);
    guest.addEventListener("did-navigate-in-page", sync);
    guest.addEventListener("did-fail-load", fail);
    return () => {
      guest.removeEventListener("dom-ready", loaded);
      guest.removeEventListener("did-start-loading", start);
      guest.removeEventListener("did-stop-loading", stop);
      guest.removeEventListener("did-navigate", sync);
      guest.removeEventListener("did-navigate-in-page", sync);
      guest.removeEventListener("did-fail-load", fail);
    };
  }, [writeState]);

  return <section className="flex min-h-0 flex-1 flex-col" aria-label={site.name}>
    <div className="flex shrink-0 items-center gap-2 border-b px-4 py-2">
      <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.back)} disabled={!ready || !canBack} onClick={() => view.current?.goBack()}><ArrowLeft className="size-4" /></Button>
      <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.forward)} disabled={!ready || !canForward} onClick={() => view.current?.goForward()}><ArrowRight className="size-4" /></Button>
      <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.reload)} disabled={!ready} onClick={() => view.current?.reload()}><RotateCw className="size-4" /></Button>
      {loading && <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" aria-label={t(($) => $.opening)} />}
      <span className="min-w-0 flex-1 truncate text-caption text-muted-foreground" title={url}>{url}</span>
      <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.open_browser)} onClick={() => openExternal(url)}><ExternalLink className="size-4" /></Button>
    </div>
    {failed && <p role="alert" className="px-4 py-3 text-body text-destructive">{t(($) => $.open_error)}</p>}
    {createElement("webview", {
      ref: view,
      src: initialUrl,
      partition: "persist:multica-web-sites",
      allowpopups: "true",
      className: "flex min-h-0 flex-1",
      title: site.name,
    })}
  </section>;
}

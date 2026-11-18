"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  filterKeymap, groupInstalledApps, installedAppsOptions, isKnownAppCategory, parseAppIntro, parseAppKeymap, useAppsViewStore,
  type AppIntro, type AppKeymap, type InstalledApp, type LocalAppsBridge,
} from "@multica/core/apps";
import { Button } from "@multica/ui/components/ui/button";
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuSeparator } from "@multica/ui/components/ui/context-menu";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { AppWindow, ChevronDown, ChevronRight, ExternalLink, FileCode, FolderOpen, Info, Keyboard, Loader2, RefreshCw, Trash2 } from "@multica/ui/icons";
import { openExternal } from "../platform/open-external";
import { useT } from "../i18n";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";

export function AppsPage({ bridge }: { bridge?: LocalAppsBridge }) {
  const { t } = useT("apps");
  return <>
    <PageHeader><h1 className="text-label font-medium">{t(($) => $.title)}</h1></PageHeader>
    {bridge ? <LocalApps bridge={bridge} /> : <p className={`${PAGE_GUTTER} py-6 text-body text-muted-foreground`}>{t(($) => $.desktop_required)}</p>}
  </>;
}

function LocalApps({ bridge }: { bridge: LocalAppsBridge }) {
  const { t } = useT("apps");
  const [search, setSearch] = useState("");
  const [menu, setMenu] = useState<{ app: InstalledApp; x: number; y: number } | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const list = useQuery(installedAppsOptions(bridge));
  const hidden = useAppsViewStore((state) => state.hidden);
  const hide = useAppsViewStore((state) => state.hide);
  const restoreAll = useAppsViewStore((state) => state.restoreAll);
  const visible = useMemo(() => (list.data?.apps ?? []).filter((app) => !hidden.includes(app.path)), [list.data, hidden]);
  const hiddenCount = (list.data?.apps.length ?? 0) - visible.length;
  const groups = useMemo(() => groupInstalledApps(visible, search), [visible, search]);
  const launch = useMutation({ mutationFn: (id: string) => bridge.launch(id) });
  const reveal = useMutation({ mutationFn: (id: string) => bridge.reveal(id) });
  const [keymapFor, setKeymapFor] = useState<InstalledApp | null>(null);
  const keymap = useMutation({ mutationFn: async (id: string) => parseAppKeymap(await bridge.keymap(id)) });
  const collapsed = useAppsViewStore((state) => state.collapsed);
  const toggleCategory = useAppsViewStore((state) => state.toggleCategory);
  const showKeymap = (app: InstalledApp) => { setKeymapFor(app); keymap.mutate(app.id); };
  const [introFor, setIntroFor] = useState<InstalledApp | null>(null);
  const intro = useMutation({ mutationFn: async (id: string) => parseAppIntro(await bridge.intro(id)) });
  const showIntro = (app: InstalledApp) => { setIntroFor(app); intro.mutate(app.id); };
  const error = list.error ?? launch.error ?? reveal.error;
  const categoryLabel = (category: string) => isKnownAppCategory(category)
    ? t(($) => $.categories[category])
    : category.replace(/^folder:/, "");

  return <div className={`flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto py-4 ${PAGE_GUTTER}`}>
    <div className="flex flex-wrap items-center gap-3">
      <Input className="max-w-xs" value={search} placeholder={t(($) => $.search)} aria-label={t(($) => $.search)}
        onChange={(event) => setSearch(event.target.value)} />
      {list.data && <span className="text-caption text-muted-foreground tabular-nums">{t(($) => $.count, { count: visible.length })}</span>}
      {hiddenCount > 0 && <Button size="sm" variant="ghost" onClick={restoreAll}>{t(($) => $.restore_hidden, { count: hiddenCount })}</Button>}
      <Button className="ml-auto" size="sm" variant="outline" disabled={list.isFetching} onClick={() => void list.refetch()}>
        {list.isFetching ? <Loader2 className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}{t(($) => $.refresh)}
      </Button>
    </div>
    {error && <p role="alert" className="break-all text-body text-destructive">{error.message}</p>}
    {list.isLoading ? <p className="text-body">{t(($) => $.loading)}</p>
      : groups.length === 0 ? <p className="text-body text-muted-foreground">{t(($) => $.empty)}</p>
      : groups.map((group) => {
        const label = categoryLabel(group.category);
        // While searching every match shows, folded or not.
        const folded = !search.trim() && collapsed.includes(group.category);
        return <section key={group.category} aria-label={label} className="flex flex-col gap-2">
          <h2 className="text-label font-medium text-muted-foreground">
            <button type="button" aria-expanded={!folded} onClick={() => toggleCategory(group.category)}
              className="flex items-center gap-1.5 rounded-md px-1 py-0.5 hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring">
              {folded ? <ChevronRight className="size-4" /> : <ChevronDown className="size-4" />}
              {label} <span className="tabular-nums">{group.apps.length}</span>
            </button>
          </h2>
          {!folded && <ul className="grid grid-cols-[repeat(auto-fill,minmax(7.5rem,1fr))] gap-2">
            {group.apps.map((app) => <li key={app.id}>
              <button type="button" title={app.kind === "service" && app.config
                  ? t(($) => $.service_hint, { name: app.name, config: app.config })
                  : t(($) => $.open_hint, { path: app.path })}
                // Double-click opens (like Finder); a single click only selects. Enter opens from the keyboard.
                onDoubleClick={() => launch.mutate(app.id)}
                onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); launch.mutate(app.id); } }}
                onContextMenu={(event) => { event.preventDefault(); setMenu({ app, x: event.clientX, y: event.clientY }); setMenuOpen(true); }}
                className="flex w-full flex-col items-center gap-1.5 rounded-lg p-2 text-center hover:bg-muted focus-visible:outline-2 focus-visible:outline-ring">
                {app.icon
                  ? <img src={app.icon} alt="" className="size-10" draggable={false} />
                  : app.kind === "service"
                    ? <Keyboard className="size-10 text-muted-foreground" />
                    : <AppWindow className="size-10 text-muted-foreground" />}
                <span className="line-clamp-2 w-full break-words text-caption">{app.name}</span>
              </button>
            </li>)}
          </ul>}
        </section>;
      })}
    {menu && <AppContextMenu app={menu.app} x={menu.x} y={menu.y} open={menuOpen} onOpenChange={setMenuOpen}
      onOpen={() => launch.mutate(menu.app.id)} onReveal={() => reveal.mutate(menu.app.id)} onKeymap={() => showKeymap(menu.app)}
      onIntro={() => showIntro(menu.app)} onDelete={() => hide(menu.app.path)} />}
    {introFor && <IntroDialog app={introFor} intro={intro.data} loading={intro.isPending} error={intro.error?.message ?? null}
      categoryLabel={categoryLabel} onClose={() => { setIntroFor(null); intro.reset(); }} />}
    {keymapFor && <KeymapDialog app={keymapFor} keymap={keymap.data} loading={keymap.isPending} error={keymap.error?.message ?? null}
      onClose={() => { setKeymapFor(null); keymap.reset(); }} />}
  </div>;
}

function IntroDialog({ app, intro, loading, error, categoryLabel, onClose }: {
  app: InstalledApp; intro: AppIntro | undefined; loading: boolean; error: string | null;
  categoryLabel: (category: string) => string; onClose: () => void;
}) {
  const { t, i18n } = useT("apps");
  const date = (iso: string | null) => (iso ? new Date(iso).toLocaleString(i18n.language) : null);
  const rows: [string, string | null][] = intro ? [
    [t(($) => $.intro_category), categoryLabel(intro.category)],
    [t(($) => $.intro_version), intro.version || null],
    [t(($) => $.intro_source), t(($) => $.sources[intro.source]) + (intro.sourceDetail ? ` (${intro.sourceDetail})` : "")],
    [t(($) => $.intro_bundle), intro.bundleId || null],
    [t(($) => $.intro_path), intro.path],
    [t(($) => $.intro_config), intro.config],
    [t(($) => $.intro_added), date(intro.addedAt)],
    [t(($) => $.intro_last_used), date(intro.lastUsedAt)],
    [t(($) => $.intro_copyright), intro.copyright],
  ] : [];
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-xl">
      <DialogHeader>
        <DialogTitle className="flex items-center gap-3">
          {app.icon ? <img src={app.icon} alt="" className="size-8" /> : <AppWindow className="size-8 text-muted-foreground" />}
          {app.name}
        </DialogTitle>
        <DialogDescription>
          {loading ? t(($) => $.loading) : intro?.description ?? (intro ? t(($) => $.intro_no_description) : "\u00a0")}
        </DialogDescription>
      </DialogHeader>
      {error && <p role="alert" className="text-body text-destructive">{error}</p>}
      {intro && <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-body">
        {rows.filter(([, value]) => value).map(([label, value]) => <div key={label} className="contents">
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="break-all">{value}</dd>
        </div>)}
      </dl>}
      {intro && <div className="flex flex-wrap gap-2">
        {intro.homepage && <Button size="sm" variant="outline" onClick={() => openExternal(intro.homepage!)}>
          <ExternalLink className="size-4" />{t(($) => $.intro_homepage)}
        </Button>}
        {!intro.description && <Button size="sm" variant="outline"
          onClick={() => openExternal(`https://www.bing.com/search?q=${encodeURIComponent(`${intro.name} ${intro.kind === "service" ? "" : "app "}${intro.bundleId}`.trim())}`)}>
          <ExternalLink className="size-4" />{t(($) => $.intro_search_web)}
        </Button>}
      </div>}
    </DialogContent>
  </Dialog>;
}

function KeymapDialog({ app, keymap, loading, error, onClose }: {
  app: InstalledApp; keymap: AppKeymap | undefined; loading: boolean; error: string | null; onClose: () => void;
}) {
  const { t } = useT("apps");
  const [query, setQuery] = useState("");
  const sections = keymap ? filterKeymap(keymap, query) : [];
  const total = keymap?.sections.reduce((n, s) => n + s.bindings.length, 0) ?? 0;
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-3xl">
      <DialogHeader>
        <DialogTitle>{t(($) => $.keymap_title, { name: app.name })}</DialogTitle>
        <DialogDescription className="break-all">{keymap ? t(($) => $.keymap_source, { path: keymap.source, count: total }) : "\u00a0"}</DialogDescription>
      </DialogHeader>
      <Input autoFocus value={query} placeholder={t(($) => $.keymap_search)} aria-label={t(($) => $.keymap_search)} onChange={(event) => setQuery(event.target.value)} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        {loading && <p className="py-4 text-body">{t(($) => $.loading)}</p>}
        {error && <p role="alert" className="py-4 text-body text-destructive">{error}</p>}
        {keymap && sections.length === 0 && <p className="py-4 text-body text-muted-foreground">{t(($) => $.keymap_empty)}</p>}
        {sections.map((section, i) => <section key={`${section.title}-${i}`} aria-label={section.title} className="py-2">
          <h3 className="mb-1 text-caption font-medium text-muted-foreground">{section.title}</h3>
          <table className="w-full text-body">
            <tbody>
              {section.bindings.map((b, j) => <tr key={j} className="border-b last:border-0 align-top">
                <td className="w-40 py-1 pr-3 whitespace-nowrap" title={b.raw}><kbd className="font-mono text-label">{b.keys}</kbd></td>
                <td className="py-1 font-mono text-caption break-all">
                  {b.app && <span className="mr-2 rounded bg-muted px-1 font-sans">{t(($) => $.keymap_in_app, { app: b.app })}</span>}
                  {b.command}
                </td>
              </tr>)}
            </tbody>
          </table>
        </section>)}
      </div>
    </DialogContent>
  </Dialog>;
}

function AppContextMenu({ app, x, y, open, onOpenChange, onOpen, onReveal, onKeymap, onIntro, onDelete }: {
  app: InstalledApp; x: number; y: number; open: boolean; onOpenChange: (open: boolean) => void;
  onOpen: () => void; onReveal: () => void; onKeymap: () => void; onIntro: () => void; onDelete: () => void;
}) {
  const { t } = useT("apps");
  const anchor = useMemo(() => ({ getBoundingClientRect: () => DOMRect.fromRect({ x, y, width: 0, height: 0 }) }), [x, y]);
  const item = (run: () => void) => () => { onOpenChange(false); run(); };
  return <ContextMenu open={open} onOpenChange={onOpenChange}>
    <ContextMenuContent anchor={anchor} aria-label={app.name}>
      {app.kind === "service"
        ? <ContextMenuItem disabled={!app.config} onClick={item(onOpen)}><FileCode />{t(($) => $.menu_open_config)}</ContextMenuItem>
        : <ContextMenuItem onClick={item(onOpen)}><AppWindow />{t(($) => $.menu_open)}</ContextMenuItem>}
      <ContextMenuItem onClick={item(onIntro)}><Info />{t(($) => $.menu_intro)}</ContextMenuItem>
      {app.keymap && <ContextMenuItem onClick={item(onKeymap)}><Keyboard />{t(($) => $.menu_keymap)}</ContextMenuItem>}
      <ContextMenuItem onClick={item(onReveal)}><FolderOpen />{t(($) => $.reveal)}</ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem variant="destructive" onClick={item(onDelete)}><Trash2 />{t(($) => $.menu_delete)}</ContextMenuItem>
    </ContextMenuContent>
  </ContextMenu>;
}

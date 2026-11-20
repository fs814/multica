"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  containersKeys, engineStatusOptions, groupContainers, isPaused, isRunning, matchesQuery, parseRelatedPlan, parseRelatedResult,
  resourceListOptions, shortId, splitCommand, splitEntries, RESOURCE_KINDS,
  type ContainerAction, type ContainerInfo, type EngineKind, type ImageInfo, type LocalContainersBridge, type NetworkInfo,
  type RelatedOptions, type ResourceKind, type VolumeInfo,
} from "@multica/core/containers";
import { Button } from "@multica/ui/components/ui/button";
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuSeparator } from "@multica/ui/components/ui/context-menu";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import {
  Copy, Download, Ellipsis, Info, Loader2, Pause, Pencil, Play, Power, RefreshCw, RotateCw, ScrollText, Square, SquareTerminal, Trash2, Zap,
} from "@multica/ui/icons";
import { useT } from "../i18n";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";

export function ContainersPage({ bridge }: { bridge?: LocalContainersBridge }) {
  const { t } = useT("containers");
  return <>
    <PageHeader><h1 className="text-label font-medium">{t(($) => $.title)}</h1></PageHeader>
    {bridge ? <LocalContainers bridge={bridge} /> : <p className={`${PAGE_GUTTER} py-6 text-body text-muted-foreground`}>{t(($) => $.desktop_required)}</p>}
  </>;
}

type Notice = { kind: "info" | "error"; text: string } | null;
type MenuTarget =
  | { type: "container"; row: ContainerInfo }
  | { type: "image"; row: ImageInfo }
  | { type: "volume"; row: VolumeInfo }
  | { type: "network"; row: NetworkInfo };
/** One confirmable operation: `run` goes through act() as a single step; `bulk` (multi-select) reports its own progress. */
interface Confirm { title: string; force?: boolean; forceLabel?: string; run?: (force: boolean) => Promise<unknown>; bulk?: (force: boolean) => void; label: string }

/** A visible row as the selection sees it: stable key, display name, and the id the engine acts on. */
interface SelRow { key: string; name: string; id: string; container?: ContainerInfo; builtin?: boolean }
const imageKey = (r: ImageInfo) => `${r.id}-${r.ref}`;
const networkKey = (r: NetworkInfo) => r.id || r.name;

/** Rows of the current tab in on-screen order (containers follow their project grouping), so a Shift range matches what you see. */
function visibleRows(tab: ResourceKind, data: unknown, search: string, runningOnly: boolean): SelRow[] {
  if (!data) return [];
  if (tab === "containers") {
    const shown = (data as ContainerInfo[]).filter((r) => (!runningOnly || isRunning(r) || isPaused(r)) && matchesQuery(r, search));
    return groupContainers(shown).flatMap((g) => g.containers).map((r) => ({ key: r.id, name: r.name, id: r.id, container: r }));
  }
  if (tab === "images") return (data as ImageInfo[]).filter((r) => matchesQuery(r, search)).map((r) => ({ key: imageKey(r), name: r.ref, id: r.ref }));
  if (tab === "volumes") return (data as VolumeInfo[]).filter((r) => matchesQuery(r, search)).map((r) => ({ key: r.name, name: r.name, id: r.name }));
  return (data as NetworkInfo[]).filter((r) => matchesQuery(r, search))
    .map((r) => ({ key: networkKey(r), name: r.name, id: networkKey(r), builtin: BUILTIN_NETWORKS.has(r.name) }));
}

/** Clicks on a row's own controls (buttons, inputs, links) never change the selection. */
const isInteractive = (target: EventTarget) => target instanceof Element && target.closest("button, input, select, textarea, a, label") !== null;

interface Selection {
  has: (key: string) => boolean;
  click: (key: string, event: React.MouseEvent) => void;
  /** Select a whole group (a compose project); Ctrl/Cmd adds it to the selection. */
  group: (keys: string[], event: React.MouseEvent) => void;
}

/** Row props shared by every table: file-manager selection (click / Ctrl-click / Shift-click) and highlight. */
function selectableRow(selection: Selection, key: string, label: string) {
  const selected = selection.has(key);
  return {
    "aria-selected": selected,
    "aria-label": label,
    className: `border-b last:border-0 ${selected ? "bg-accent" : "hover:bg-accent/40"}`,
    // Shift+mousedown would otherwise extend the browser's text selection across the table.
    onMouseDown: (event: React.MouseEvent) => { if (event.shiftKey && !isInteractive(event.target)) event.preventDefault(); },
    onClick: (event: React.MouseEvent) => selection.click(key, event),
  };
}

/** Built-in networks every engine refuses to remove. */
const BUILTIN_NETWORKS = new Set(["bridge", "host", "none", "podman", "default"]);

function formatDate(value: string, locale: string): string {
  if (!value) return "";
  const date = new Date(value);
  // Docker prints "2026-11-18 10:00:00 +0800 CST", which Date cannot parse: show it trimmed.
  return Number.isNaN(date.getTime()) ? value.slice(0, 19) : date.toLocaleString(locale);
}

const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error));

function LocalContainers({ bridge }: { bridge: LocalContainersBridge }) {
  const { t, i18n } = useT("containers");
  const queryClient = useQueryClient();
  const status = useQuery(engineStatusOptions(bridge));
  const active = status.data?.active ?? null;
  const activeEngine = status.data?.engines.find((e) => e.kind === active) ?? null;
  const [tab, setTab] = useState<ResourceKind>("containers");
  const [search, setSearch] = useState("");
  const [runningOnly, setRunningOnly] = useState(false);
  const [pullRef, setPullRef] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<Notice>(null);
  const [menu, setMenu] = useState<{ target: MenuTarget; x: number; y: number } | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [confirm, setConfirm] = useState<Confirm | null>(null);
  const [runFor, setRunFor] = useState<{ image: string; interactive: boolean } | null>(null);
  const [logsFor, setLogsFor] = useState<ContainerInfo | null>(null);
  const [inspectFor, setInspectFor] = useState<{ resource: ResourceKind; id: string; name: string } | null>(null);
  const [renameFor, setRenameFor] = useState<ContainerInfo | null>(null);
  // Removing containers goes through one dialog (single row or a selection)
  // that can also take the images / volumes / networks only they use.
  const [removeFor, setRemoveFor] = useState<{ ids: string[]; live: boolean } | null>(null);
  const removeContainers = (rows: ContainerInfo[]) => {
    if (rows.length) setRemoveFor({ ids: rows.map((r) => r.id), live: rows.some((r) => isRunning(r) || isPaused(r)) });
  };
  const enabled = Boolean(activeEngine?.running);
  const lists = {
    containers: useQuery(resourceListOptions(bridge, active, "containers", enabled && tab === "containers")),
    images: useQuery(resourceListOptions(bridge, active, "images", enabled && tab === "images")),
    volumes: useQuery(resourceListOptions(bridge, active, "volumes", enabled && tab === "volumes")),
    networks: useQuery(resourceListOptions(bridge, active, "networks", enabled && tab === "networks")),
  };
  const current = lists[tab];

  // Multi-select, file-manager style: click selects one row, Ctrl/Cmd-click
  // toggles a row, Shift-click selects from the anchor (last plain or Ctrl
  // click) to here, Ctrl+Shift-click adds that range. Bulk actions act on the
  // selected rows that are still visible, so filtering never touches hidden ones.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [anchor, setAnchor] = useState<string | null>(null);
  const visible = useMemo(() => visibleRows(tab, current.data, search, runningOnly), [tab, current.data, search, runningOnly]);
  const chosen = visible.filter((r) => selected.has(r.key));
  const clearSelection = () => { setSelected(new Set()); setAnchor(null); };
  const selection: Selection = {
    has: (key) => selected.has(key),
    click: (key, event) => {
      if (isInteractive(event.target)) return;
      const additive = event.ctrlKey || event.metaKey;
      const from = event.shiftKey && anchor !== null ? visible.findIndex((r) => r.key === anchor) : -1;
      const to = from === -1 ? -1 : visible.findIndex((r) => r.key === key);
      if (to !== -1) {
        const range = visible.slice(Math.min(from, to), Math.max(from, to) + 1).map((r) => r.key);
        setSelected((prev) => new Set(additive ? [...prev, ...range] : range));
        return; // the anchor stays put, so the range can be re-dragged from it
      }
      setAnchor(key);
      setSelected((prev) => {
        if (!additive) return new Set([key]);
        const next = new Set(prev);
        if (next.has(key)) next.delete(key); else next.add(key);
        return next;
      });
    },
    group: (keys, event) => {
      if (keys.length === 0) return;
      const additive = event.ctrlKey || event.metaKey;
      setAnchor(keys[0]!);
      setSelected((prev) => new Set(additive ? [...prev, ...keys] : keys));
    },
  };

  const refresh = () => queryClient.invalidateQueries({ queryKey: containersKeys.all });
  /** Runs one operation per selected row, one at a time, with a live "what: done of total" line; failures are listed after. */
  async function bulk(what: string, rows: SelRow[], run: (row: SelRow) => Promise<unknown>) {
    setNotice(null);
    const failures: string[] = [];
    let ok = 0;
    try {
      for (const [i, row] of rows.entries()) {
        setBusy(t(($) => $.bulk_progress, { what, done: i, total: rows.length }));
        try {
          await run(row);
          ok += 1;
        } catch (error) {
          failures.push(`${row.name}: ${errorText(error)}`);
        }
      }
    } finally {
      setBusy(null);
      const result = t(($) => $.bulk_result, { what, ok, total: rows.length });
      setNotice(failures.length ? { kind: "error", text: [result, ...failures].join("\n") } : { kind: "info", text: result });
      await refresh();
    }
  }
  const bulkContainers = (action: ContainerAction) => {
    void bulk(t(($) => $.actions[action]), chosen, (row) => bridge.containerAction(row.id, action));
  };
  const bulkRemove = () => {
    const rows = chosen.filter((r) => !r.builtin);
    if (rows.length === 0) return;
    if (tab === "containers") { removeContainers(rows.flatMap((r) => (r.container ? [r.container] : []))); return; }
    const resource = tab;
    setConfirm({
      title: t(($) => $.confirm_remove_selected, { count: rows.length }), label: t(($) => $.actions.remove),
      force: false, forceLabel: resource === "networks" ? undefined : t(($) => $.force),
      bulk: (force) => void bulk(t(($) => $.actions.remove), rows, (row) => bridge.remove(resource, row.id, force)),
    });
  };
  /** Containers plus, as chosen, their images / volumes / networks; one summary line, then any failures. */
  const removeWithRelated = (ids: string[], options: RelatedOptions) => act(t(($) => $.actions.remove), async () => {
    const result = parseRelatedResult(await bridge.removeWithRelated(ids, options));
    const { removed, failed } = result;
    const summary = t(($) => $.related_result, {
      containers: removed.containers.length, images: removed.images.length, volumes: removed.volumes.length, networks: removed.networks.length,
    });
    if (failed.length) throw new Error([summary, ...failed.map((f) => `${f.name}: ${f.error}`)].join("\n"));
    clearSelection();
    return summary;
  }, (summary) => String(summary));
  /** Runs one engine operation, reports the outcome, and refreshes every list. */
  async function act(label: string, run: () => Promise<unknown>, success?: (result: unknown) => string) {
    setBusy(label);
    setNotice(null);
    try {
      const result = await run();
      setNotice({ kind: "info", text: success ? success(result) : t(($) => $.done, { what: label }) });
    } catch (error) {
      setNotice({ kind: "error", text: errorText(error) });
    } finally {
      setBusy(null);
      await refresh();
    }
  }
  const terminal = (label: string, run: () => Promise<unknown>) => act(label, run, () => t(($) => $.terminal_opened, { what: label }));
  const openMenu = (target: MenuTarget, x: number, y: number) => { setMenu({ target, x, y }); setMenuOpen(true); };

  const containerAction = (row: ContainerInfo, action: ContainerAction) =>
    act(`${t(($) => $.actions[action])} ${row.name}`, () => bridge.containerAction(row.id, action));
  const actions: Actions = {
    action: containerAction,
    kill: (row) => setConfirm({ title: t(($) => $.confirm_kill, { name: row.name }), label: t(($) => $.actions.kill), run: () => bridge.containerAction(row.id, "kill") }),
    shell: (row, sh) => terminal(`${row.name} ${sh}`, () => bridge.exec(row.id, sh)),
    attach: (row) => terminal(`${row.name}`, () => bridge.attach(row.id)),
    logs: setLogsFor,
    inspect: (resource, id, name) => setInspectFor({ resource, id, name }),
    rename: setRenameFor,
    copy: (id) => {
      void navigator.clipboard?.writeText(id).then(() => setNotice({ kind: "info", text: t(($) => $.copied, { what: shortId(id) }) }));
    },
    removeContainer: (row) => removeContainers([row]),
    remove: (resource, id, name) => setConfirm({
      title: resource === "volumes" ? t(($) => $.confirm_remove_volume, { name }) : t(($) => $.confirm_remove, { name }),
      label: t(($) => $.actions.remove), force: false, forceLabel: resource === "networks" ? undefined : t(($) => $.force),
      run: (force) => bridge.remove(resource, id, force),
    }),
    run: (image, interactive) => setRunFor({ image, interactive }),
  };

  const engines = status.data?.engines ?? [];
  const startEngine = (kind: EngineKind) => act(t(($) => $.starting_engine, { engine: kind }), () => bridge.startEngine(kind), (r) => (typeof r === "string" && r.trim() ? r.trim().split("\n").pop()! : t(($) => $.done, { what: kind })));
  const selectEngine = (kind: EngineKind) => act(kind, () => bridge.useEngine(kind), () => t(($) => $.done, { what: kind }));

  return <div className={`${PAGE_GUTTER} flex min-h-0 flex-1 flex-col gap-3 py-4`}>
    <div className="flex flex-wrap items-center gap-2 text-body">
      {status.isPending && <span className="text-muted-foreground">{t(($) => $.loading)}</span>}
      {status.data && engines.length === 0 && <span role="alert" className="text-destructive">{t(($) => $.no_engine)}</span>}
      {engines.length > 0 && <>
        <label className="flex items-center gap-2">
          <span className="text-muted-foreground">{t(($) => $.engine)}</span>
          <select aria-label={t(($) => $.engine)} className="rounded border bg-background px-2 py-1" value={active ?? ""}
            onChange={(event) => selectEngine(event.target.value as EngineKind)}>
            {engines.map((e) => <option key={e.kind} value={e.kind}>{e.kind}{e.version ? ` — ${e.version}` : ""}</option>)}
          </select>
        </label>
        {activeEngine && <span className={`flex items-center gap-1.5 ${activeEngine.running ? "text-emerald-600" : "text-destructive"}`}>
          <span aria-hidden className={`size-2 rounded-full ${activeEngine.running ? "bg-emerald-500" : "bg-destructive"}`} />
          {activeEngine.running ? t(($) => $.engine_running) : t(($) => $.engine_stopped)}
        </span>}
        {activeEngine && !activeEngine.running && <Button size="sm" disabled={Boolean(busy)} onClick={() => startEngine(activeEngine.kind)}>
          <Power className="size-4" />{t(($) => $.start_engine, { engine: activeEngine.kind })}
        </Button>}
      </>}
      <Button size="sm" variant="outline" className="ml-auto" onClick={() => { void status.refetch(); void refresh(); }} aria-label={t(($) => $.refresh)}>
        <RefreshCw className="size-4" />{t(($) => $.refresh)}
      </Button>
    </div>
    {activeEngine && !activeEngine.running && activeEngine.error && <p className="text-body text-muted-foreground">
      {t(($) => $.engine_down, { engine: activeEngine.kind, error: activeEngine.error })}
    </p>}

    <div className="flex flex-wrap items-center gap-2">
      <div role="tablist" className="flex rounded-md border p-0.5">
        {RESOURCE_KINDS.map((kind) => <button key={kind} role="tab" type="button" aria-selected={tab === kind}
          className={`rounded px-3 py-1 text-body ${tab === kind ? "bg-accent font-medium" : "text-muted-foreground"}`}
          onClick={() => { setTab(kind); setSearch(""); clearSelection(); }}>{t(($) => $.tabs[kind])}</button>)}
      </div>
      <Input className="h-8 w-56" value={search} placeholder={t(($) => $.search)} aria-label={t(($) => $.search)} onChange={(e) => setSearch(e.target.value)} />
      {tab === "containers" && <label className="flex items-center gap-1.5 text-body">
        <input type="checkbox" checked={runningOnly} onChange={(e) => setRunningOnly(e.target.checked)} />{t(($) => $.running_only)}
      </label>}
      {tab === "images" && <form className="flex items-center gap-1.5" onSubmit={(event) => {
        event.preventDefault();
        const ref = pullRef.trim();
        if (ref) void act(t(($) => $.pulling, { ref }), () => bridge.pull(ref), () => t(($) => $.done, { what: `${t(($) => $.actions.pull)} ${ref}` })).then(() => setPullRef(""));
      }}>
        <Input className="h-8 w-64" value={pullRef} placeholder={t(($) => $.pull_placeholder)} aria-label={t(($) => $.pull_placeholder)} onChange={(e) => setPullRef(e.target.value)} />
        <Button size="sm" type="submit" variant="outline" disabled={!enabled || !pullRef.trim() || Boolean(busy)}><Download className="size-4" />{t(($) => $.actions.pull)}</Button>
      </form>}
      <Button size="sm" variant="outline" disabled={!enabled || Boolean(busy)} onClick={() => setConfirm({
        title: t(($) => $.prunes[tab]), label: t(($) => $.prune), force: false,
        forceLabel: tab === "images" ? t(($) => $.prune_images_all) : undefined,
        run: (all) => bridge.prune(tab, all),
      })}><Trash2 className="size-4" />{t(($) => $.prune)}</Button>
    </div>

    {chosen.length > 0 && <div role="toolbar" aria-label={t(($) => $.selected, { count: chosen.length })} className="flex flex-wrap items-center gap-2 rounded-md border bg-muted/30 px-3 py-1.5">
      <span className="text-label font-medium tabular-nums">{t(($) => $.selected, { count: chosen.length })}</span>
      {tab === "containers" && <>
        <Button size="sm" variant="outline" disabled={Boolean(busy)} onClick={() => bulkContainers("start")}><Play className="size-4" />{t(($) => $.actions.start)}</Button>
        <Button size="sm" variant="outline" disabled={Boolean(busy)} onClick={() => bulkContainers("stop")}><Square className="size-4" />{t(($) => $.actions.stop)}</Button>
        <Button size="sm" variant="outline" disabled={Boolean(busy)} onClick={() => bulkContainers("restart")}><RotateCw className="size-4" />{t(($) => $.actions.restart)}</Button>
      </>}
      <Button size="sm" variant="destructive" disabled={Boolean(busy) || chosen.every((r) => r.builtin)} onClick={bulkRemove}>
        <Trash2 className="size-4" />{t(($) => $.actions.remove)}
      </Button>
      <Button size="sm" variant="ghost" className="ml-auto" onClick={clearSelection}>{t(($) => $.clear_selection)}</Button>
    </div>}
    {busy && <p role="status" className="flex items-center gap-2 text-body text-muted-foreground"><Loader2 className="size-4 animate-spin" />{busy}</p>}
    {!busy && notice && <p role={notice.kind === "error" ? "alert" : "status"} className={`whitespace-pre-line text-body ${notice.kind === "error" ? "text-destructive" : "text-muted-foreground"}`}>{notice.text}</p>}
    {current.error && <p role="alert" className="text-body text-destructive">{current.error.message}</p>}

    <div className="min-h-0 flex-1 overflow-auto">
      {enabled && current.isPending && <p className="text-body text-muted-foreground">{t(($) => $.loading)}</p>}
      {tab === "containers" && lists.containers.data && <ContainersTable rows={lists.containers.data} search={search} runningOnly={runningOnly} actions={actions} openMenu={openMenu} locale={i18n.language} selection={selection} />}
      {tab === "images" && lists.images.data && <ImagesTable rows={lists.images.data} search={search} actions={actions} openMenu={openMenu} locale={i18n.language} selection={selection} />}
      {tab === "volumes" && lists.volumes.data && <SimpleTable rows={lists.volumes.data} search={search} selection={selection} rowName={(r) => r.name}
        columns={[[t(($) => $.columns.name), (r) => r.name], [t(($) => $.columns.driver), (r) => r.driver], [t(($) => $.columns.mountpoint), (r) => <span className="break-all font-mono text-caption">{r.mountpoint}</span>]]}
        rowKey={(r) => r.name} onMenu={(row, x, y) => openMenu({ type: "volume", row }, x, y)}
        buttons={(r) => <>
          <IconButton label={t(($) => $.actions.inspect)} onClick={() => actions.inspect("volumes", r.name, r.name)}><Info /></IconButton>
          <IconButton label={t(($) => $.actions.remove)} onClick={() => actions.remove("volumes", r.name, r.name)}><Trash2 /></IconButton>
        </>} />}
      {tab === "networks" && lists.networks.data && <SimpleTable rows={lists.networks.data} search={search} selection={selection} rowName={(r) => r.name}
        columns={[[t(($) => $.columns.name), (r) => r.name], [t(($) => $.columns.driver), (r) => r.driver], [t(($) => $.columns.scope), (r) => r.scope], [t(($) => $.columns.id), (r) => <span className="font-mono text-caption">{shortId(r.id)}</span>]]}
        rowKey={networkKey} onMenu={(row, x, y) => openMenu({ type: "network", row }, x, y)}
        buttons={(r) => <>
          <IconButton label={t(($) => $.actions.inspect)} onClick={() => actions.inspect("networks", r.id || r.name, r.name)}><Info /></IconButton>
          <IconButton label={t(($) => $.actions.remove)} disabled={BUILTIN_NETWORKS.has(r.name)} onClick={() => actions.remove("networks", r.id || r.name, r.name)}><Trash2 /></IconButton>
        </>} />}
    </div>

    {menu && <RowMenu target={menu.target} x={menu.x} y={menu.y} open={menuOpen} onOpenChange={setMenuOpen} actions={actions} />}
    {confirm && <ConfirmDialog confirm={confirm} onClose={() => setConfirm(null)} onConfirm={(force) => {
      const c = confirm;
      setConfirm(null);
      if (c.bulk) { c.bulk(force); return; }
      if (c.run) void act(c.label, () => c.run!(force), (r) => (typeof r === "string" && r.trim() ? r.trim().split("\n").slice(-1)[0]! : t(($) => $.done, { what: c.label })));
    }} />}
    {runFor && <RunDialog image={runFor.image} interactive={runFor.interactive} onClose={() => setRunFor(null)} onRun={(request) => {
      setRunFor(null);
      if (request.interactive) void terminal(request.image, () => bridge.run(request));
      else void act(`${t(($) => $.run_submit)} ${request.image}`, () => bridge.run(request), (id) => t(($) => $.run_started, { id: shortId(String(id)) }));
    }} />}
    {logsFor && <LogsDialog bridge={bridge} container={logsFor} onClose={() => setLogsFor(null)} />}
    {inspectFor && <InspectDialog bridge={bridge} target={inspectFor} onClose={() => setInspectFor(null)} />}
    {removeFor && <RemoveRelatedDialog bridge={bridge} ids={removeFor.ids} live={removeFor.live} onClose={() => setRemoveFor(null)}
      onRemove={(options) => { const ids = removeFor.ids; setRemoveFor(null); void removeWithRelated(ids, options); }} />}
    {renameFor && <RenameDialog container={renameFor} onClose={() => setRenameFor(null)} onRename={(name) => {
      const row = renameFor;
      setRenameFor(null);
      void act(`${t(($) => $.actions.rename)} ${row.name} → ${name}`, () => bridge.renameContainer(row.id, name));
    }} />}
  </div>;
}

interface Actions {
  action: (row: ContainerInfo, action: ContainerAction) => void;
  kill: (row: ContainerInfo) => void;
  shell: (row: ContainerInfo, sh: string) => void;
  attach: (row: ContainerInfo) => void;
  logs: (row: ContainerInfo) => void;
  inspect: (resource: ResourceKind, id: string, name: string) => void;
  rename: (row: ContainerInfo) => void;
  copy: (id: string) => void;
  removeContainer: (row: ContainerInfo) => void;
  remove: (resource: "images" | "volumes" | "networks", id: string, name: string) => void;
  run: (image: string, interactive: boolean) => void;
}
type OpenMenu = (target: MenuTarget, x: number, y: number) => void;

function IconButton({ label, onClick, disabled, children }: { label: string; onClick: () => void; disabled?: boolean; children: ReactNode }) {
  return <Button size="icon-sm" variant="ghost" aria-label={label} title={label} disabled={disabled} onClick={onClick}>{children}</Button>;
}

function MoreButton({ onOpen }: { onOpen: (x: number, y: number) => void }) {
  const { t } = useT("containers");
  return <IconButton label={t(($) => $.more)} onClick={() => {
    const el = document.activeElement as HTMLElement | null;
    const rect = el?.getBoundingClientRect();
    onOpen(rect ? rect.left : 0, rect ? rect.bottom : 0);
  }}><Ellipsis /></IconButton>;
}

function StateBadge({ row }: { row: ContainerInfo }) {
  const { t } = useT("containers");
  const color = isRunning(row) ? "bg-emerald-500" : isPaused(row) ? "bg-amber-500" : row.state === "dead" ? "bg-destructive" : "bg-muted-foreground/50";
  return <span className="flex items-center gap-1.5 whitespace-nowrap"><span aria-hidden className={`size-2 rounded-full ${color}`} />{t(($) => $.states[row.state])}</span>;
}

const th = "px-2 py-1.5 text-left text-caption font-medium text-muted-foreground";
const td = "px-2 py-1.5 align-top";

function ContainersTable({ rows, search, runningOnly, actions, openMenu, locale, selection }: {
  rows: ContainerInfo[]; search: string; runningOnly: boolean; actions: Actions; openMenu: OpenMenu; locale: string; selection: Selection;
}) {
  const { t } = useT("containers");
  const shown = rows.filter((r) => (!runningOnly || isRunning(r) || isPaused(r)) && matchesQuery(r, search));
  const groups = groupContainers(shown);
  const hasProjects = groups.some((g) => g.project);
  return <>
    <p className="mb-1 text-caption text-muted-foreground">{t(($) => $.shown, { shown: shown.length, total: rows.length })}</p>
    {shown.length === 0 && <p className="text-body text-muted-foreground">{t(($) => $.empty)}</p>}
    {shown.length > 0 && <table className="w-full text-body">
      <thead><tr className="border-b">
        <th className={th}>{t(($) => $.columns.name)}</th><th className={th}>{t(($) => $.columns.image)}</th>
        <th className={th}>{t(($) => $.columns.status)}</th><th className={th}>{t(($) => $.columns.ports)}</th>
        <th className={th}>{t(($) => $.columns.created)}</th><th className={th}>{t(($) => $.columns.actions)}</th>
      </tr></thead>
      {groups.map((group) => <tbody key={group.project ?? ""} aria-label={group.project ?? t(($) => $.no_project)}>
        {hasProjects && <tr className="cursor-pointer" title={t(($) => $.select_group)} onClick={(event) => selection.group(group.containers.map((c) => c.id), event)}>
          <td colSpan={6} className="px-2 pt-3 pb-1 text-caption font-medium">{group.project ?? t(($) => $.no_project)}</td>
        </tr>}
        {group.containers.map((row) => <tr key={row.id} {...selectableRow(selection, row.id, t(($) => $.select_row, { name: row.name }))}
          onContextMenu={(event) => { event.preventDefault(); openMenu({ type: "container", row }, event.clientX, event.clientY); }}
          onDoubleClick={() => actions.logs(row)}>
          <td className={td}><div className="font-medium">{row.name}</div><div className="font-mono text-caption text-muted-foreground" title={row.id}>{shortId(row.id)}</div></td>
          <td className={`${td} break-all`}>{row.image}</td>
          <td className={td}><StateBadge row={row} /><div className="text-caption text-muted-foreground">{row.status}</div></td>
          <td className={`${td} font-mono text-caption break-all`}>{row.ports}</td>
          <td className={`${td} text-caption whitespace-nowrap`}>{formatDate(row.createdAt, locale)}</td>
          <td className={`${td} whitespace-nowrap`}>
            {isRunning(row) && <>
              <IconButton label={`${t(($) => $.actions.stop)} ${row.name}`} onClick={() => actions.action(row, "stop")}><Square /></IconButton>
              <IconButton label={`${t(($) => $.actions.restart)} ${row.name}`} onClick={() => actions.action(row, "restart")}><RotateCw /></IconButton>
              <IconButton label={`${t(($) => $.actions.shell)} ${row.name}`} onClick={() => actions.shell(row, "sh")}><SquareTerminal /></IconButton>
            </>}
            {isPaused(row) && <IconButton label={`${t(($) => $.actions.unpause)} ${row.name}`} onClick={() => actions.action(row, "unpause")}><Play /></IconButton>}
            {!isRunning(row) && !isPaused(row) && <>
              <IconButton label={`${t(($) => $.actions.start)} ${row.name}`} onClick={() => actions.action(row, "start")}><Play /></IconButton>
              <IconButton label={`${t(($) => $.actions.attach)} ${row.name}`} onClick={() => actions.attach(row)}><SquareTerminal /></IconButton>
            </>}
            <IconButton label={`${t(($) => $.actions.logs)} ${row.name}`} onClick={() => actions.logs(row)}><ScrollText /></IconButton>
            <MoreButton onOpen={(x, y) => openMenu({ type: "container", row }, x, y)} />
          </td>
        </tr>)}
      </tbody>)}
    </table>}
  </>;
}

function ImagesTable({ rows, search, actions, openMenu, locale, selection }: {
  rows: ImageInfo[]; search: string; actions: Actions; openMenu: OpenMenu; locale: string; selection: Selection;
}) {
  const { t } = useT("containers");
  const shown = rows.filter((r) => matchesQuery(r, search));
  return <>
    <p className="mb-1 text-caption text-muted-foreground">{t(($) => $.shown, { shown: shown.length, total: rows.length })}</p>
    {shown.length === 0 && <p className="text-body text-muted-foreground">{t(($) => $.empty)}</p>}
    {shown.length > 0 && <table className="w-full text-body">
      <thead><tr className="border-b">
        <th className={th}>{t(($) => $.columns.repository)}</th><th className={th}>{t(($) => $.columns.tag)}</th><th className={th}>{t(($) => $.columns.id)}</th>
        <th className={th}>{t(($) => $.columns.size)}</th><th className={th}>{t(($) => $.columns.created)}</th>
        <th className={th}>{t(($) => $.columns.used_by)}</th><th className={th}>{t(($) => $.columns.actions)}</th>
      </tr></thead>
      <tbody>{shown.map((row) => <tr key={imageKey(row)} {...selectableRow(selection, imageKey(row), t(($) => $.select_row, { name: row.ref }))}
        onContextMenu={(event) => { event.preventDefault(); openMenu({ type: "image", row }, event.clientX, event.clientY); }}
        onDoubleClick={() => actions.run(row.ref, true)}>
        <td className={`${td} break-all`}>{row.repository}</td><td className={td}>{row.tag}</td>
        <td className={`${td} font-mono text-caption`} title={row.id}>{shortId(row.id)}</td>
        <td className={`${td} whitespace-nowrap`}>{row.size}</td><td className={`${td} text-caption whitespace-nowrap`}>{formatDate(row.createdAt, locale)}</td>
        <td className={td}>{row.containers ?? ""}</td>
        <td className={`${td} whitespace-nowrap`}>
          <IconButton label={`${t(($) => $.actions.run_interactive)} ${row.ref}`} onClick={() => actions.run(row.ref, true)}><SquareTerminal /></IconButton>
          <IconButton label={`${t(($) => $.actions.run_background)} ${row.ref}`} onClick={() => actions.run(row.ref, false)}><Play /></IconButton>
          <IconButton label={`${t(($) => $.actions.remove)} ${row.ref}`} onClick={() => actions.remove("images", row.ref, row.ref)}><Trash2 /></IconButton>
          <MoreButton onOpen={(x, y) => openMenu({ type: "image", row }, x, y)} />
        </td>
      </tr>)}</tbody>
    </table>}
  </>;
}

function SimpleTable<R extends object>({ rows, search, columns, rowKey, rowName, buttons, onMenu, selection }: {
  rows: R[]; search: string; columns: [string, (row: R) => ReactNode][]; rowKey: (row: R) => string; rowName: (row: R) => string;
  buttons: (row: R) => ReactNode; onMenu: (row: R, x: number, y: number) => void; selection: Selection;
}) {
  const { t } = useT("containers");
  const shown = rows.filter((r) => matchesQuery(r, search));
  return <>
    <p className="mb-1 text-caption text-muted-foreground">{t(($) => $.shown, { shown: shown.length, total: rows.length })}</p>
    {shown.length === 0 && <p className="text-body text-muted-foreground">{t(($) => $.empty)}</p>}
    {shown.length > 0 && <table className="w-full text-body">
      <thead><tr className="border-b">{columns.map(([label]) => <th key={label} className={th}>{label}</th>)}<th className={th}>{t(($) => $.columns.actions)}</th></tr></thead>
      <tbody>{shown.map((row) => <tr key={rowKey(row)} {...selectableRow(selection, rowKey(row), t(($) => $.select_row, { name: rowName(row) }))}
        onContextMenu={(event) => { event.preventDefault(); onMenu(row, event.clientX, event.clientY); }}>
        {columns.map(([label, cell]) => <td key={label} className={td}>{cell(row)}</td>)}
        <td className={`${td} whitespace-nowrap`}>{buttons(row)}</td>
      </tr>)}</tbody>
    </table>}
  </>;
}

function RowMenu({ target, x, y, open, onOpenChange, actions }: {
  target: MenuTarget; x: number; y: number; open: boolean; onOpenChange: (open: boolean) => void; actions: Actions;
}) {
  const { t } = useT("containers");
  const anchor = useMemo(() => ({ getBoundingClientRect: () => DOMRect.fromRect({ x, y, width: 0, height: 0 }) }), [x, y]);
  const item = (run: () => void) => () => { onOpenChange(false); run(); };
  let items: ReactNode;
  let name: string;
  if (target.type === "container") {
    const row = target.row;
    name = row.name;
    const running = isRunning(row);
    const paused = isPaused(row);
    items = <>
      {!running && !paused && <ContextMenuItem onClick={item(() => actions.action(row, "start"))}><Play />{t(($) => $.actions.start)}</ContextMenuItem>}
      {!running && !paused && <ContextMenuItem onClick={item(() => actions.attach(row))}><SquareTerminal />{t(($) => $.actions.attach)}</ContextMenuItem>}
      {(running || paused) && <ContextMenuItem onClick={item(() => actions.action(row, "stop"))}><Square />{t(($) => $.actions.stop)}</ContextMenuItem>}
      <ContextMenuItem onClick={item(() => actions.action(row, "restart"))}><RotateCw />{t(($) => $.actions.restart)}</ContextMenuItem>
      {running && <ContextMenuItem onClick={item(() => actions.action(row, "pause"))}><Pause />{t(($) => $.actions.pause)}</ContextMenuItem>}
      {paused && <ContextMenuItem onClick={item(() => actions.action(row, "unpause"))}><Play />{t(($) => $.actions.unpause)}</ContextMenuItem>}
      {(running || paused) && <ContextMenuItem onClick={item(() => actions.kill(row))}><Zap />{t(($) => $.actions.kill)}</ContextMenuItem>}
      <ContextMenuSeparator />
      {running && <ContextMenuItem onClick={item(() => actions.shell(row, "sh"))}><SquareTerminal />{t(($) => $.actions.shell)}</ContextMenuItem>}
      {running && <ContextMenuItem onClick={item(() => actions.shell(row, "bash"))}><SquareTerminal />{t(($) => $.actions.shell_bash)}</ContextMenuItem>}
      <ContextMenuItem onClick={item(() => actions.logs(row))}><ScrollText />{t(($) => $.actions.logs)}</ContextMenuItem>
      <ContextMenuItem onClick={item(() => actions.inspect("containers", row.id, row.name))}><Info />{t(($) => $.actions.inspect)}</ContextMenuItem>
      <ContextMenuItem onClick={item(() => actions.rename(row))}><Pencil />{t(($) => $.actions.rename)}</ContextMenuItem>
      <ContextMenuItem onClick={item(() => actions.copy(row.id))}><Copy />{t(($) => $.actions.copy_id)}</ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem variant="destructive" onClick={item(() => actions.removeContainer(row))}><Trash2 />{t(($) => $.actions.remove)}</ContextMenuItem>
    </>;
  } else if (target.type === "image") {
    const row = target.row;
    name = row.ref;
    items = <>
      <ContextMenuItem onClick={item(() => actions.run(row.ref, true))}><SquareTerminal />{t(($) => $.actions.run_interactive)}</ContextMenuItem>
      <ContextMenuItem onClick={item(() => actions.run(row.ref, false))}><Play />{t(($) => $.actions.run_background)}</ContextMenuItem>
      <ContextMenuItem onClick={item(() => actions.inspect("images", row.ref, row.ref))}><Info />{t(($) => $.actions.inspect)}</ContextMenuItem>
      <ContextMenuItem onClick={item(() => actions.copy(row.id))}><Copy />{t(($) => $.actions.copy_id)}</ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem variant="destructive" onClick={item(() => actions.remove("images", row.ref, row.ref))}><Trash2 />{t(($) => $.actions.remove)}</ContextMenuItem>
    </>;
  } else {
    const resource = target.type === "volume" ? "volumes" : "networks";
    const id = target.type === "volume" ? target.row.name : target.row.id || target.row.name;
    name = target.row.name;
    items = <>
      <ContextMenuItem onClick={item(() => actions.inspect(resource, id, name))}><Info />{t(($) => $.actions.inspect)}</ContextMenuItem>
      <ContextMenuItem onClick={item(() => actions.copy(id))}><Copy />{t(($) => $.actions.copy_id)}</ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem variant="destructive" disabled={target.type === "network" && BUILTIN_NETWORKS.has(name)}
        onClick={item(() => actions.remove(resource, id, name))}><Trash2 />{t(($) => $.actions.remove)}</ContextMenuItem>
    </>;
  }
  return <ContextMenu open={open} onOpenChange={onOpenChange}>
    <ContextMenuContent anchor={anchor} aria-label={name}>{items}</ContextMenuContent>
  </ContextMenu>;
}

function ConfirmDialog({ confirm, onClose, onConfirm }: { confirm: Confirm; onClose: () => void; onConfirm: (force: boolean) => void }) {
  const { t } = useT("containers");
  const [force, setForce] = useState(Boolean(confirm.force));
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader><DialogTitle>{confirm.title}</DialogTitle><DialogDescription>{" "}</DialogDescription></DialogHeader>
      {confirm.forceLabel && <label className="flex items-center gap-2 text-body">
        <input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />{confirm.forceLabel}
      </label>}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>{t(($) => $.cancel)}</Button>
        <Button variant="destructive" onClick={() => onConfirm(force)}>{confirm.label}</Button>
      </div>
    </DialogContent>
  </Dialog>;
}

/**
 * Remove containers and, optionally, the images / volumes / networks that only
 * they use. The plan comes from Desktop (it inspects every container), so the
 * lists show exactly what would go, and what is kept because another
 * container still uses it. Volumes default off: they hold data.
 */
function RemoveRelatedDialog({ bridge, ids, live, onClose, onRemove }: {
  bridge: LocalContainersBridge; ids: string[]; live: boolean; onClose: () => void; onRemove: (options: RelatedOptions) => void;
}) {
  const { t } = useT("containers");
  const plan = useQuery({
    queryKey: [...containersKeys.all, "related", ...ids],
    queryFn: async () => parseRelatedPlan(await bridge.relatedPlan(ids)),
    staleTime: 0,
  });
  const [force, setForce] = useState(live);
  const [images, setImages] = useState(true);
  const [volumes, setVolumes] = useState(false);
  const [networks, setNetworks] = useState(true);
  const p = plan.data;
  const names = (list: string[]) => (list.length ? list.join(", ") : t(($) => $.related_none));
  const kind = (k: "image" | "volume" | "network") => t(($) => $.tabs[`${k}s`]);
  const option = (label: string, checked: boolean, set: (on: boolean) => void, list: string[]) =>
    <label className="flex items-start gap-2">
      <input type="checkbox" className="mt-1" checked={checked && list.length > 0} disabled={list.length === 0} onChange={(e) => set(e.target.checked)} />
      <span className="min-w-0"><span className="block">{label}</span>
        <span className="block break-all font-mono text-caption text-muted-foreground">{names(list)}</span></span>
    </label>;
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-lg">
      <DialogHeader><DialogTitle>{t(($) => $.remove_related_title, { count: ids.length })}</DialogTitle><DialogDescription>{" "}</DialogDescription></DialogHeader>
      {plan.isPending && <p className="flex items-center gap-2 text-body text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t(($) => $.loading)}</p>}
      {plan.error && <p role="alert" className="text-body text-destructive">{plan.error.message}</p>}
      {p && <div className="flex flex-col gap-2 text-body">
        <div><span className="text-muted-foreground">{t(($) => $.related_containers)}: </span>
          <span className="break-all font-mono text-caption">{names(p.containers.map((c) => c.name))}</span></div>
        {option(t(($) => $.related_images), images, setImages, p.images.map((i) => i.name))}
        {option(t(($) => $.related_volumes), volumes, setVolumes, p.volumes)}
        {option(t(($) => $.related_networks), networks, setNetworks, p.networks)}
        {p.kept.length > 0 && <div className="text-caption text-muted-foreground">
          <p>{t(($) => $.related_kept)}</p>
          <ul className="ml-4 list-disc">{p.kept.map((k) => <li key={`${k.kind}:${k.name}`} className="break-all">
            {kind(k.kind)} · {t(($) => $.related_used_by, { name: k.name, users: k.usedBy.join(", ") })}
          </li>)}</ul>
        </div>}
        <label className="flex items-center gap-2"><input type="checkbox" checked={force} onChange={(e) => setForce(e.target.checked)} />{t(($) => $.force)}</label>
      </div>}
      <div className="flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>{t(($) => $.cancel)}</Button>
        <Button variant="destructive" disabled={!p} onClick={() => onRemove({
          images: images && (p?.images.length ?? 0) > 0, volumes: volumes && (p?.volumes.length ?? 0) > 0,
          networks: networks && (p?.networks.length ?? 0) > 0, force,
        })}>{t(($) => $.actions.remove)}</Button>
      </div>
    </DialogContent>
  </Dialog>;
}

function RunDialog({ image, interactive: initial, onClose, onRun }: {
  image: string; interactive: boolean; onClose: () => void; onRun: (request: Parameters<LocalContainersBridge["run"]>[0]) => void;
}) {
  const { t } = useT("containers");
  const [interactive, setInteractive] = useState(initial);
  const [remove, setRemove] = useState(initial);
  const [name, setName] = useState("");
  const [command, setCommand] = useState("");
  const [ports, setPorts] = useState("");
  const [env, setEnv] = useState("");
  const [volumes, setVolumes] = useState("");
  const [error, setError] = useState<string | null>(null);
  const field = "w-full rounded border bg-background px-2 py-1 font-mono text-caption";
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-lg">
      <DialogHeader><DialogTitle>{t(($) => $.run_title, { image })}</DialogTitle><DialogDescription>{" "}</DialogDescription></DialogHeader>
      <form className="flex flex-col gap-2 text-body" onSubmit={(event) => {
        event.preventDefault();
        try {
          onRun({
            image, interactive, remove, name: name.trim() || undefined, command: splitCommand(command),
            ports: splitEntries(ports), env: splitEntries(env, false), volumes: splitEntries(volumes, false),
          });
        } catch (e) {
          setError(errorText(e));
        }
      }}>
        <label className="flex items-center gap-2"><input type="radio" name="mode" checked={interactive} onChange={() => { setInteractive(true); setRemove(true); }} />{t(($) => $.run_interactive)}</label>
        <label className="flex items-center gap-2"><input type="radio" name="mode" checked={!interactive} onChange={() => { setInteractive(false); setRemove(false); }} />{t(($) => $.run_background)}</label>
        <label className="flex items-center gap-2"><input type="checkbox" checked={remove} onChange={(e) => setRemove(e.target.checked)} />{t(($) => $.run_remove)}</label>
        <label>{t(($) => $.run_name)}<input className={field} value={name} onChange={(e) => setName(e.target.value)} /></label>
        <label>{t(($) => $.run_command)}<input className={field} value={command} onChange={(e) => setCommand(e.target.value)} /></label>
        <label>{t(($) => $.run_ports)}<input className={field} value={ports} onChange={(e) => setPorts(e.target.value)} /></label>
        <label>{t(($) => $.run_env)}<textarea className={field} rows={2} value={env} onChange={(e) => setEnv(e.target.value)} /></label>
        <label>{t(($) => $.run_volumes)}<textarea className={field} rows={2} value={volumes} onChange={(e) => setVolumes(e.target.value)} /></label>
        {error && <p role="alert" className="text-destructive">{error}</p>}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose}>{t(($) => $.cancel)}</Button>
          <Button type="submit">{t(($) => $.run_submit)}</Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>;
}

const TAIL_CHOICES = [100, 500, 2000, 10000];

function LogsDialog({ bridge, container, onClose }: { bridge: LocalContainersBridge; container: ContainerInfo; onClose: () => void }) {
  const { t } = useT("containers");
  const [tail, setTail] = useState(500);
  const [timestamps, setTimestamps] = useState(false);
  const logs = useQuery({
    queryKey: [...containersKeys.all, "logs", container.id, tail, timestamps],
    queryFn: async () => String(await bridge.logs(container.id, tail, timestamps)),
    staleTime: 0,
  });
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-4xl">
      <DialogHeader><DialogTitle>{t(($) => $.logs_title, { name: container.name })}</DialogTitle><DialogDescription>{container.image}</DialogDescription></DialogHeader>
      <div className="flex flex-wrap items-center gap-3 text-body">
        <label className="flex items-center gap-1.5">{t(($) => $.logs_lines)}
          <select aria-label={t(($) => $.logs_lines)} className="rounded border bg-background px-1 py-0.5" value={tail} onChange={(e) => setTail(Number(e.target.value))}>
            {TAIL_CHOICES.map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </label>
        <label className="flex items-center gap-1.5"><input type="checkbox" checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} />{t(($) => $.logs_timestamps)}</label>
        <Button size="sm" variant="outline" onClick={() => void logs.refetch()}><RefreshCw className="size-4" />{t(($) => $.refresh)}</Button>
      </div>
      {logs.error && <p role="alert" className="text-body text-destructive">{logs.error.message}</p>}
      <LogText text={logs.data} loading={logs.isFetching} />
    </DialogContent>
  </Dialog>;
}

function LogText({ text, loading }: { text: string | undefined; loading: boolean }) {
  const { t } = useT("containers");
  const [el, setEl] = useState<HTMLPreElement | null>(null);
  // Logs read bottom-up: keep the newest lines in view.
  useEffect(() => { if (el) el.scrollTop = el.scrollHeight; }, [el, text]);
  return <pre ref={setEl} aria-label={t(($) => $.actions.logs)} className="min-h-40 flex-1 overflow-auto rounded bg-muted p-2 font-mono text-caption whitespace-pre-wrap">
    {text === undefined ? (loading ? t(($) => $.loading) : "") : text || t(($) => $.logs_empty)}
  </pre>;
}

function InspectDialog({ bridge, target, onClose }: { bridge: LocalContainersBridge; target: { resource: ResourceKind; id: string; name: string }; onClose: () => void }) {
  const { t } = useT("containers");
  const details = useQuery({
    queryKey: [...containersKeys.all, "inspect", target.resource, target.id],
    queryFn: async () => String(await bridge.inspect(target.resource, target.id)),
  });
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-4xl">
      <DialogHeader><DialogTitle>{t(($) => $.inspect_title, { name: target.name })}</DialogTitle><DialogDescription>{target.id}</DialogDescription></DialogHeader>
      <div><Button size="sm" variant="outline" disabled={!details.data} onClick={() => void navigator.clipboard?.writeText(details.data ?? "")}><Copy className="size-4" />{t(($) => $.copy)}</Button></div>
      {details.error && <p role="alert" className="text-body text-destructive">{details.error.message}</p>}
      <pre aria-label={t(($) => $.actions.inspect)} className="min-h-40 flex-1 overflow-auto rounded bg-muted p-2 font-mono text-caption">{details.data ?? t(($) => $.loading)}</pre>
    </DialogContent>
  </Dialog>;
}

function RenameDialog({ container, onClose, onRename }: { container: ContainerInfo; onClose: () => void; onRename: (name: string) => void }) {
  const { t } = useT("containers");
  const [name, setName] = useState(container.name);
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader><DialogTitle>{t(($) => $.rename_title, { name: container.name })}</DialogTitle><DialogDescription>{" "}</DialogDescription></DialogHeader>
      <form className="flex flex-col gap-2" onSubmit={(event) => { event.preventDefault(); if (name.trim() && name.trim() !== container.name) onRename(name.trim()); }}>
        <Input autoFocus aria-label={t(($) => $.rename_label)} value={name} onChange={(e) => setName(e.target.value)} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose}>{t(($) => $.cancel)}</Button>
          <Button type="submit">{t(($) => $.actions.rename)}</Button>
        </div>
      </form>
    </DialogContent>
  </Dialog>;
}

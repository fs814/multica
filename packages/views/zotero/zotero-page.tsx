"use client";

import { useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ZOTERO_KINDS, ZOTERO_SOURCES, countZoteroSources, filterZoteroFiles, parseZoteroCollection, parseZoteroFile, parseZoteroImportResult,
  zoteroCatalogOptions, zoteroCollectionsOptions, zoteroKeys, zoteroStatusOptions,
  type LocalZoteroBridge, type ZoteroFile, type ZoteroFileFilter, type ZoteroImportResult,
} from "@multica/core/zotero";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuSeparator } from "@multica/ui/components/ui/context-menu";
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@multica/ui/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { Input } from "@multica/ui/components/ui/input";
import { ChevronDown, Eye, FolderOpen, Loader2, Pencil, Plus, RefreshCw, Trash2 } from "@multica/ui/icons";
import { useT } from "../i18n";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";

export function ZoteroPage({ bridge }: { bridge?: LocalZoteroBridge }) {
  const { t } = useT("zotero");
  return <>
    <PageHeader><h1 className="text-label font-medium">{t(($) => $.title)}</h1></PageHeader>
    {bridge ? <LocalZotero bridge={bridge} /> : <p className={`${PAGE_GUTTER} py-6 text-body text-muted-foreground`}>{t(($) => $.desktop_required)}</p>}
  </>;
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`;
}

interface TransferRun { done: number; total: number; results: ZoteroImportResult[]; running: boolean }

function LocalZotero({ bridge }: { bridge: LocalZoteroBridge }) {
  const { t } = useT("zotero");
  const client = useQueryClient();
  const [filter, setFilter] = useState<ZoteroFileFilter>({ source: "all", kind: "all", search: "", hideImported: false });
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [trash, setTrash] = useState(false);
  const [run, setRun] = useState<TransferRun | null>(null);

  const catalog = useQuery(zoteroCatalogOptions(bridge));
  const status = useQuery(zoteroStatusOptions(bridge));
  const reachable = status.data?.state === "ready" || status.data?.state === "needs_key";
  const collections = useQuery(zoteroCollectionsOptions(bridge, reachable));
  const files = useMemo(() => catalog.data?.files ?? [], [catalog.data]);
  const counts = useMemo(() => countZoteroSources(files), [files]);
  const shown = useMemo(() => filterZoteroFiles(files, filter), [files, filter]);
  const byId = useMemo(() => new Map(files.map((f) => [f.id, f])), [files]);
  const chosen = [...selected].filter((id) => byId.has(id));
  const allShownSelected = shown.length > 0 && shown.every((f) => selected.has(f.id));
  const someShownSelected = shown.some((f) => selected.has(f.id));

  const authorize = useMutation({
    mutationFn: () => bridge.authorize(),
    onSettled: () => client.invalidateQueries({ queryKey: zoteroKeys.status }),
  });

  const transfer = useMutation({
    mutationFn: async (target: { key: string } | { path: string }) => {
      const key = "key" in target ? target.key : parseZoteroCollection(await bridge.ensureCollection(target.path)).key;
      const ids = chosen;
      const results: ZoteroImportResult[] = [];
      setRun({ done: 0, total: ids.length, results, running: true });
      for (const id of ids) {
        results.push(parseZoteroImportResult(await bridge.importFile(id, key, trash)));
        setRun({ done: results.length, total: ids.length, results: [...results], running: true });
      }
      setRun({ done: results.length, total: ids.length, results, running: false });
      return results;
    },
    onSuccess: (results) => {
      const done = new Set(results.filter((r) => r.status !== "failed").map((r) => r.id));
      setSelected((prev) => new Set([...prev].filter((id) => !done.has(id))));
    },
    onSettled: () => {
      void client.invalidateQueries({ queryKey: zoteroKeys.catalog });
      void client.invalidateQueries({ queryKey: zoteroKeys.collections });
      void client.invalidateQueries({ queryKey: zoteroKeys.status });
    },
  });

  const toggle = (id: string, on: boolean) => setSelected((prev) => {
    const next = new Set(prev);
    if (on) next.add(id); else next.delete(id);
    return next;
  });
  const toggleShown = (on: boolean) => setSelected((prev) => {
    const next = new Set(prev);
    for (const f of shown) if (on) next.add(f.id); else next.delete(f.id);
    return next;
  });
  const refresh = () => {
    void catalog.refetch();
    void status.refetch();
    if (reachable) void collections.refetch();
  };

  const preview = useMutation({ mutationFn: (id: string) => bridge.preview(id) });
  const [trashed, setTrashed] = useState<{ count: number; failed: { id: string; error: string }[] } | null>(null);
  const moveToTrash = useMutation({
    mutationFn: async (ids: string[]) => {
      let count = 0;
      const failed: { id: string; error: string }[] = [];
      for (const id of ids) {
        try {
          await bridge.trash(id);
          count += 1;
        } catch (e) {
          failed.push({ id, error: (e as Error).message });
        }
      }
      return { count, failed, ids };
    },
    onSuccess: ({ count, failed, ids }) => {
      setTrashed({ count, failed });
      const gone = new Set(ids.filter((id) => !failed.some((f) => f.id === id)));
      setSelected((prev) => new Set([...prev].filter((id) => !gone.has(id))));
    },
    onSettled: () => client.invalidateQueries({ queryKey: zoteroKeys.catalog }),
  });
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [menu, setMenu] = useState<{ file: ZoteroFile; x: number; y: number } | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const rename = useMutation({
    mutationFn: async ({ id, name }: { id: string; name: string }) => ({ id, file: parseZoteroFile(await bridge.rename(id, name)) }),
    onSuccess: ({ id, file }) => {
      setRenamingId(null);
      // The id follows the path, so carry a selection over to the renamed entry.
      setSelected((prev) => (prev.has(id) ? new Set([...prev].filter((x) => x !== id).concat(file.id)) : prev));
    },
    // On failure the editor closes too; the reason shows in the error banner.
    onError: () => setRenamingId(null),
    onSettled: () => client.invalidateQueries({ queryKey: zoteroKeys.catalog }),
  });
  const openMenu = (file: ZoteroFile, event: React.MouseEvent) => {
    event.preventDefault();
    setMenu({ file, x: event.clientX, y: event.clientY });
    setMenuOpen(true);
  };
  const error = catalog.error ?? status.error ?? collections.error ?? authorize.error ?? transfer.error ?? preview.error ?? moveToTrash.error ?? rename.error;
  const canTransfer = reachable && chosen.length > 0 && !transfer.isPending;
  const state = status.data?.state;
  const statusText = state === "ready" ? t(($) => $.status_ready) : state === "needs_key" ? t(($) => $.status_needs_key)
    : state === "disabled" ? t(($) => $.status_disabled) : state === "not_running" ? t(($) => $.status_not_running) : "";

  return <div className={`flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto py-4 ${PAGE_GUTTER}`}>
    <div className="flex flex-wrap items-center justify-between gap-2">
      <p role="status" className={`min-w-0 text-body ${state === "ready" ? "text-muted-foreground" : "text-warning"}`}>{statusText}</p>
      <div className="flex gap-2">
        {state === "needs_key" && <Button size="sm" disabled={authorize.isPending} onClick={() => authorize.mutate()}>
          {authorize.isPending && <Loader2 className="size-4 animate-spin" />}
          {authorize.isPending ? t(($) => $.authorizing) : t(($) => $.authorize)}
        </Button>}
        <Button size="sm" variant="outline" disabled={catalog.isFetching} onClick={refresh}>
          <RefreshCw className="size-4" />{t(($) => $.refresh)}
        </Button>
      </div>
    </div>
    {error && <p role="alert" className="break-all text-body text-destructive">{error.message}</p>}

    <div className="flex flex-wrap items-center gap-2">
      {(["all", ...ZOTERO_SOURCES] as const).map((source) => (
        <Button key={source} size="sm" variant={filter.source === source ? "secondary" : "ghost"} aria-pressed={filter.source === source}
          onClick={() => setFilter((f) => ({ ...f, source }))}>
          {t(($) => $.sources[source])} <span className="text-muted-foreground tabular-nums">{counts[source]}</span>
        </Button>
      ))}
      <span className="mx-1 h-5 w-px bg-border" aria-hidden />
      {(["all", ...ZOTERO_KINDS] as const).map((kind) => (
        <Button key={kind} size="sm" variant={filter.kind === kind ? "secondary" : "ghost"} aria-pressed={filter.kind === kind}
          onClick={() => setFilter((f) => ({ ...f, kind }))}>
          {t(($) => $.kinds[kind])}
        </Button>
      ))}
    </div>
    <div className="flex flex-wrap items-center gap-3">
      <Input className="max-w-xs" value={filter.search} placeholder={t(($) => $.search)} aria-label={t(($) => $.search)}
        onChange={(event) => setFilter((f) => ({ ...f, search: event.target.value }))} />
      <label className="flex items-center gap-2 text-body">
        <Checkbox checked={filter.hideImported} onCheckedChange={(on) => setFilter((f) => ({ ...f, hideImported: on === true }))} />
        {t(($) => $.hide_imported)}
      </label>
    </div>

    <TransferBar
      count={chosen.length} canTransfer={canTransfer} pending={transfer.isPending}
      collections={collections.data ?? []} trash={trash} setTrash={setTrash}
      onTransfer={(target) => transfer.mutate(target)}
      onTrash={() => moveToTrash.mutate(chosen)} trashPending={moveToTrash.isPending}
    />
    {run && <TransferSummary run={run} byId={byId} />}
    {trashed && <div role="status" className="text-body">
      <p>{t(($) => $.trashed_result, { count: trashed.count })}</p>
      {trashed.failed.length > 0 && <ul className="mt-1 text-caption text-destructive">
        {trashed.failed.map((f) => <li key={f.id} className="break-all">{t(($) => $.failed_file, { name: byId.get(f.id)?.name ?? f.id, error: f.error })}</li>)}
      </ul>}
    </div>}

    {catalog.data && catalog.data.errors.length > 0 && <details className="text-caption text-muted-foreground">
      <summary>{t(($) => $.scan_errors)}</summary>
      <ul>{catalog.data.errors.map((e) => <li key={e} className="break-all">{e}</li>)}</ul>
    </details>}

    {catalog.isLoading ? <p className="text-body">{t(($) => $.loading)}</p>
      : catalog.data && catalog.data.roots.length === 0 ? <p className="text-body text-muted-foreground">{t(($) => $.no_sources)}</p>
      : <div className="min-h-0 overflow-auto rounded-lg border">
        <table className="w-full text-left text-body">
          <thead className="sticky top-0 bg-background text-caption text-muted-foreground">
            <tr className="border-b">
              <th className="w-10 px-3 py-2">
                <Checkbox aria-label={t(($) => $.select_all)} checked={allShownSelected} indeterminate={!allShownSelected && someShownSelected}
                  onCheckedChange={(on) => toggleShown(on === true)} />
              </th>
              <th className="px-2 py-2 font-medium">{t(($) => $.column_name)}</th>
              <th className="px-2 py-2 font-medium">{t(($) => $.column_source)}</th>
              <th className="px-2 py-2 font-medium">{t(($) => $.column_month)}</th>
              <th className="px-2 py-2 text-right font-medium">{t(($) => $.column_size)}</th>
              <th className="px-2 py-2 font-medium">{t(($) => $.column_zotero)}</th>
              <th className="w-10" />
            </tr>
          </thead>
          <tbody>
            {shown.map((file) => <FileRow key={file.id} file={file} checked={selected.has(file.id)} toggle={toggle} reveal={() => void bridge.reveal(file.id)} preview={() => preview.mutate(file.id)}
              onTrash={() => moveToTrash.mutate([file.id])} trashPending={moveToTrash.isPending} onContextMenu={(event) => openMenu(file, event)}
              renaming={renamingId === file.id} onRename={(name) => (name && name !== file.name ? rename.mutate({ id: file.id, name }) : setRenamingId(null))}
              onCancelRename={() => setRenamingId(null)} />)}
          </tbody>
        </table>
        {shown.length === 0 && <p className="px-3 py-6 text-body text-muted-foreground">{t(($) => $.empty)}</p>}
      </div>}
    {menu && <FileContextMenu file={menu.file} x={menu.x} y={menu.y} open={menuOpen} onOpenChange={setMenuOpen}
      onPreview={() => preview.mutate(menu.file.id)} onRename={() => { rename.reset(); setRenamingId(menu.file.id); }}
      onReveal={() => void bridge.reveal(menu.file.id)} onTrash={() => moveToTrash.mutate([menu.file.id])} />}
  </div>;
}

/** One right-click menu for the whole list, anchored at the cursor (same pattern as the issue list). */
function FileContextMenu({ file, x, y, open, onOpenChange, onPreview, onRename, onReveal, onTrash }: {
  file: ZoteroFile; x: number; y: number; open: boolean; onOpenChange: (open: boolean) => void;
  onPreview: () => void; onRename: () => void; onReveal: () => void; onTrash: () => void;
}) {
  const { t } = useT("zotero");
  const anchor = useMemo(() => ({ getBoundingClientRect: () => DOMRect.fromRect({ x, y, width: 0, height: 0 }) }), [x, y]);
  const item = (run: () => void) => () => { onOpenChange(false); run(); };
  return <ContextMenu open={open} onOpenChange={onOpenChange}>
    <ContextMenuContent anchor={anchor} aria-label={file.name}>
      <ContextMenuItem onClick={item(onPreview)}><Eye />{t(($) => $.menu_preview)}</ContextMenuItem>
      <ContextMenuItem onClick={item(onRename)}><Pencil />{t(($) => $.menu_rename)}</ContextMenuItem>
      <ContextMenuItem onClick={item(onReveal)}><FolderOpen />{t(($) => $.reveal)}</ContextMenuItem>
      <ContextMenuSeparator />
      <ContextMenuItem variant="destructive" onClick={item(onTrash)}><Trash2 />{t(($) => $.menu_trash)}</ContextMenuItem>
    </ContextMenuContent>
  </ContextMenu>;
}

/** Inline name editor: Enter or leaving the field saves, Esc cancels. The stem is pre-selected, like Finder. */
function RenameInput({ file, onRename, onCancel }: { file: ZoteroFile; onRename: (name: string) => void; onCancel: () => void }) {
  const { t } = useT("zotero");
  const [value, setValue] = useState(file.name);
  const done = useRef(false);
  const finish = (save: boolean) => {
    if (done.current) return;
    done.current = true;
    if (save) onRename(value.trim()); else onCancel();
  };
  return <Input autoFocus className="h-7" value={value} aria-label={t(($) => $.rename_label, { name: file.name })}
    onFocus={(event) => {
      const dot = file.name.lastIndexOf(".");
      event.currentTarget.setSelectionRange(0, dot > 0 ? dot : file.name.length);
    }}
    onChange={(event) => setValue(event.target.value)}
    onKeyDown={(event) => {
      if (event.key === "Enter") { event.preventDefault(); finish(true); }
      if (event.key === "Escape") { event.preventDefault(); finish(false); }
    }}
    onBlur={() => finish(true)} />;
}

function FileRow({ file, checked, toggle, reveal, preview, onTrash, trashPending, onContextMenu, renaming, onRename, onCancelRename }: {
  file: ZoteroFile; checked: boolean; toggle: (id: string, on: boolean) => void; reveal: () => void; preview: () => void;
  onTrash: () => void; trashPending: boolean; onContextMenu: (event: React.MouseEvent) => void;
  renaming: boolean; onRename: (name: string) => void; onCancelRename: () => void;
}) {
  const { t } = useT("zotero");
  return <tr className="border-b last:border-0 hover:bg-muted/50" onContextMenu={renaming ? undefined : onContextMenu}>
    <td className="px-3 py-1.5">
      <Checkbox aria-label={t(($) => $.select_file, { name: file.name })} checked={checked} onCheckedChange={(on) => toggle(file.id, on === true)} />
    </td>
    <td className="max-w-[28rem] px-2 py-1.5">{renaming
      ? <RenameInput file={file} onRename={onRename} onCancel={onCancelRename} />
      : <span className="block cursor-default truncate select-none" title={t(($) => $.preview_hint, { path: file.path })}
        onDoubleClick={preview}>{file.name}</span>}</td>
    <td className="px-2 py-1.5 whitespace-nowrap text-muted-foreground">{t(($) => $.sources[file.source])}</td>
    <td className="px-2 py-1.5 whitespace-nowrap text-muted-foreground tabular-nums">{file.month}</td>
    <td className="px-2 py-1.5 text-right whitespace-nowrap text-muted-foreground tabular-nums">{formatSize(file.size)}</td>
    <td className="px-2 py-1.5 whitespace-nowrap text-caption">
      {file.imported ? <span className="text-brand">{file.imported.collection ? t(($) => $.in_zotero, { collection: file.imported.collection }) : t(($) => $.in_zotero_unfiled)}</span> : null}
    </td>
    <td className="px-2 py-1.5">
      <div className="flex items-center justify-end gap-0.5">
        <Button size="icon-sm" variant="ghost" aria-label={t(($) => $.reveal)} title={t(($) => $.reveal)} onClick={reveal}><FolderOpen className="size-4" /></Button>
        <TrashButton label={t(($) => $.trash_file, { name: file.name })} disabled={trashPending} onConfirm={onTrash} />
      </div>
    </td>
  </tr>;
}

/**
 * Two-step trash: the first click arms the button ("Move to Trash?"), the second
 * moves the file. Leaving the button disarms it. Trash is recoverable, so no dialog.
 */
function TrashButton({ label, disabled, onConfirm, children }: {
  label: string; disabled?: boolean; onConfirm: () => void; children?: React.ReactNode;
}) {
  const { t } = useT("zotero");
  const [armed, setArmed] = useState(false);
  if (armed) {
    return <Button size="sm" variant="destructive" autoFocus disabled={disabled} onBlur={() => setArmed(false)}
      onClick={() => { setArmed(false); onConfirm(); }}>
      <Trash2 className="size-4" />{t(($) => $.trash_confirm)}
    </Button>;
  }
  return children
    ? <Button size="sm" variant="outline" disabled={disabled} onClick={() => setArmed(true)}><Trash2 className="size-4" />{children}</Button>
    : <Button size="icon-sm" variant="ghost" aria-label={label} title={label} disabled={disabled} onClick={() => setArmed(true)}><Trash2 className="size-4" /></Button>;
}

/** Collections whose path contains every word of the query (case-insensitive), so "llm pap" finds "Papers/LLM". */
export function matchCollections<T extends { path: string }>(collections: readonly T[], query: string): T[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  return collections.filter((c) => words.every((w) => c.path.toLowerCase().includes(w)));
}

function TransferBar({ count, canTransfer, pending, collections, trash, setTrash, onTransfer, onTrash, trashPending }: {
  count: number; canTransfer: boolean; pending: boolean; collections: { key: string; path: string }[];
  trash: boolean; setTrash: (on: boolean) => void;
  onTransfer: (target: { key: string } | { path: string }) => void;
  onTrash: () => void; trashPending: boolean;
}) {
  const { t } = useT("zotero");
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const matches = matchCollections(collections, query);
  const typed = query.trim().replace(/\s*\/\s*/g, "/").replace(/^\/+|\/+$/g, "");
  const exact = collections.some((c) => c.path.toLowerCase() === typed.toLowerCase());
  const choose = (target: { key: string } | { path: string }) => {
    setOpen(false);
    setQuery("");
    onTransfer(target);
  };
  return <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/30 px-3 py-2">
    <span className="text-label font-medium tabular-nums">{t(($) => $.selected, { count })}</span>
    <Popover open={open} onOpenChange={(next) => { setOpen(next); if (!next) setQuery(""); }}>
      <PopoverTrigger render={<Button size="sm" disabled={!canTransfer} />}>
        {pending && <Loader2 className="size-4 animate-spin" />}{t(($) => $.transfer)}<ChevronDown className="size-4" />
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80 p-0">
        <Command shouldFilter={false} label={t(($) => $.collection_search)}>
          <CommandInput value={query} onValueChange={setQuery} placeholder={t(($) => $.collection_search)} />
          <CommandList>
            {matches.length === 0 && !typed && <CommandEmpty>{t(($) => $.no_collections)}</CommandEmpty>}
            {matches.length > 0 && <CommandGroup heading={t(($) => $.transfer_to)}>
              {matches.map((c) => <CommandItem key={c.key} value={c.key} onSelect={() => choose({ key: c.key })}>
                <span className="min-w-0 flex-1 truncate">{c.path}</span>
              </CommandItem>)}
            </CommandGroup>}
            {typed && !exact && <CommandGroup>
              <CommandItem value={`create:${typed}`} onSelect={() => choose({ path: typed })}>
                <Plus className="size-4" /><span className="min-w-0 flex-1 truncate">{t(($) => $.create_collection, { name: typed })}</span>
              </CommandItem>
            </CommandGroup>}
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
    <label className="flex items-center gap-2 text-body">
      <Checkbox checked={trash} onCheckedChange={(on) => setTrash(on === true)} />
      {t(($) => $.trash_originals)}
    </label>
    <div className="ml-auto">
      <TrashButton label={t(($) => $.trash_selected, { count })} disabled={count === 0 || pending || trashPending} onConfirm={onTrash}>
        {t(($) => $.trash_selected, { count })}
      </TrashButton>
    </div>
  </div>;
}

function TransferSummary({ run, byId }: { run: TransferRun; byId: Map<string, ZoteroFile> }) {
  const { t } = useT("zotero");
  const count = (s: ZoteroImportResult["status"]) => run.results.filter((r) => r.status === s).length;
  const failed = run.results.filter((r) => r.status === "failed");
  const trashed = run.results.filter((r) => r.trashed).length;
  return <div role="status" className="text-body">
    {run.running ? <p className="flex items-center gap-2"><Loader2 className="size-4 animate-spin" />{t(($) => $.progress, { done: run.done, total: run.total })}</p>
      : <p>{t(($) => $.result, { imported: count("imported"), already: count("already_imported"), failed: failed.length })}
        {trashed > 0 && <> {t(($) => $.result_trashed, { count: trashed })}</>}</p>}
    {failed.length > 0 && <ul className="mt-1 text-caption text-destructive">
      {failed.map((r) => <li key={r.id} className="break-all">{t(($) => $.failed_file, { name: byId.get(r.id)?.name ?? r.id, error: r.error })}</li>)}
    </ul>}
  </div>;
}

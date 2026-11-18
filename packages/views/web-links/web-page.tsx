"use client";

import { useId, useMemo, useState, type FormEvent } from "react";
import { ChevronDown, ChevronRight, ExternalLink, Globe, Pencil, Plus, Trash2 } from "@multica/ui/icons";
import { normalizeWebUrl, useWebLinksStore, type WebLink } from "@multica/core/web-links";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";
import { openExternal } from "../platform/open-external";

export function WebPage({ openSite }: { openSite?: (url: string) => Promise<void> }) {
  const { t } = useT("web-links");
  const links = useWebLinksStore((state) => state.links);
  const save = useWebLinksStore((state) => state.save);
  const remove = useWebLinksStore((state) => state.remove);
  const collapsed = useWebLinksStore((state) => state.collapsed);
  const toggleCategory = useWebLinksStore((state) => state.toggleCategory);
  const [editing, setEditing] = useState<WebLink | null>(null);
  const [error, setError] = useState<"save_error" | "open_error" | null>(null);
  const [opening, setOpening] = useState<string | null>(null);
  const groups = useMemo(() => groupWebLinks(links), [links]);
  const categories = useMemo(() => [...new Set(links.flatMap((link) => (link.category ? [link.category] : [])))], [links]);

  const open = async (link: WebLink) => {
    setError(null);
    if (!openSite) { openExternal(link.url); return; }
    setOpening(link.id);
    try { await openSite(link.url); }
    catch { setError("open_error"); }
    finally { setOpening(null); }
  };

  return <>
    <PageHeader><h1 className="text-label font-medium">{t(($) => $.title)}</h1></PageHeader>
    <div className={`flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto py-4 ${PAGE_GUTTER}`}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-caption text-muted-foreground">{t(($) => $.saved_locally)}</p>
        <Button size="sm" onClick={() => { setError(null); setEditing({ id: crypto.randomUUID(), name: "", url: "" }); }}>
          <Plus className="size-4" />{t(($) => $.add)}
        </Button>
      </div>
      {error && <p role="alert" className="text-body text-destructive">{error === "open_error" ? t(($) => $.open_error) : t(($) => $.save_error)}</p>}
      {editing && <WebLinkForm key={editing.id} link={editing} categories={categories} cancel={() => setEditing(null)} save={(link) => {
        try { save(link); setEditing(null); setError(null); }
        catch { setError("save_error"); }
      }} />}
      {links.length === 0 && !editing && <p className="py-8 text-body text-muted-foreground">{t(($) => $.empty)}</p>}
      {groups.map((group) => {
        const key = group.category ?? "";
        const label = group.category ?? t(($) => $.uncategorized);
        // With a single group there is nothing to fold; it always shows.
        const folded = groups.length > 1 && collapsed.includes(key);
        return <section key={key} aria-label={label} className="flex flex-col gap-3">
        {groups.length > 1 && <h2 className="text-label font-medium text-muted-foreground">
          <button type="button" aria-expanded={!folded} onClick={() => toggleCategory(key)}
            className="flex items-center gap-1.5 rounded-md px-1 py-0.5 hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring">
            {folded ? <ChevronRight className="size-4" /> : <ChevronDown className="size-4" />}
            {label} <span className="tabular-nums">{group.links.length}</span>
          </button>
        </h2>}
      {!folded && <ul className="grid gap-3 xl:grid-cols-2">
        {group.links.map((link) => <li key={link.id} className="flex min-w-0 flex-col gap-3 rounded-lg border p-4">
          <div className="flex min-w-0 items-start gap-3">
            <Globe className="mt-0.5 size-5 shrink-0 text-muted-foreground" />
            <div className="min-w-0 flex-1">
              <h3 className="break-words text-label font-medium">{link.name}</h3>
              <p className="break-all text-caption text-muted-foreground">{link.url}</p>
            </div>
            <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.edit_named, { name: link.name })} onClick={() => { setError(null); setEditing(link); }}><Pencil className="size-4" /></Button>
            <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.remove_named, { name: link.name })} onClick={() => {
              try { remove(link.id); if (editing?.id === link.id) setEditing(null); setError(null); }
              catch { setError("save_error"); }
            }}><Trash2 className="size-4" /></Button>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" disabled={opening !== null} aria-busy={opening === link.id} onClick={() => void open(link)}>
              <Globe className="size-4" />{opening === link.id ? t(($) => $.opening) : openSite ? t(($) => $.open) : t(($) => $.open_browser)}
            </Button>
            {openSite && <Button size="sm" variant="outline" onClick={() => openExternal(link.url)}><ExternalLink className="size-4" />{t(($) => $.open_browser)}</Button>}
          </div>
        </li>)}
      </ul>}
      </section>;
      })}
    </div>
  </>;
}

/** Uncategorized links first, then each category in the order it first appears. */
export function groupWebLinks(links: readonly WebLink[]): { category: string | null; links: WebLink[] }[] {
  const groups = new Map<string | null, WebLink[]>();
  if (links.some((link) => !link.category)) groups.set(null, []);
  for (const link of links) {
    const key = link.category ?? null;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(link);
  }
  return [...groups].map(([category, items]) => ({ category, links: items }));
}

function WebLinkForm({ link, categories, save, cancel }: { link: WebLink; categories: string[]; save: (link: WebLink) => void; cancel: () => void }) {
  const { t } = useT("web-links");
  const listId = useId();
  const [name, setName] = useState(link.name);
  const [category, setCategory] = useState(link.category ?? "");
  const [url, setUrl] = useState(link.url);
  const [invalid, setInvalid] = useState(false);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    const normalized = normalizeWebUrl(url);
    if (!name.trim() || !normalized) { setInvalid(true); return; }
    save({ id: link.id, name: name.trim(), url: normalized, category: category.trim() || undefined });
  };
  return <form onSubmit={submit} className="flex flex-col gap-3 rounded-lg border p-4">
    <label className="flex flex-col gap-1 text-label">{t(($) => $.name)}<Input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /></label>
    <label className="flex flex-col gap-1 text-label">{t(($) => $.url)}<Input required type="url" placeholder="https://jenkins.example.com" value={url} onChange={(event) => setUrl(event.target.value)} aria-invalid={invalid || undefined} aria-describedby={invalid ? "web-link-url-error" : undefined} /></label>
    <label className="flex flex-col gap-1 text-label">{t(($) => $.category)}<Input maxLength={60} list={listId} placeholder={t(($) => $.category_placeholder)} value={category} onChange={(event) => setCategory(event.target.value)} /></label>
    <datalist id={listId}>{categories.map((c) => <option key={c} value={c} />)}</datalist>
    {invalid && <p id="web-link-url-error" role="alert" className="text-body text-destructive">{t(($) => $.invalid_url)}</p>}
    <div className="flex gap-2"><Button type="submit" size="sm">{t(($) => $.save)}</Button><Button type="button" size="sm" variant="outline" onClick={cancel}>{t(($) => $.cancel)}</Button></div>
  </form>;
}

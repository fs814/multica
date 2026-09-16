"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { BookOpen } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@multica/ui/components/ui/dialog";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogAction, AlertDialogCancel } from "@multica/ui/components/ui/alert-dialog";
import { Input } from "@multica/ui/components/ui/input";
import { memoryOptions, memoryKey, memoryProblem, MemoryError, saveMemory, memoryDraftKey, useMemoryDraftStore, setMemoryDraft, captureMemoryDraft, updateCapturedMemoryDraft, isMemoryDraftSessionCurrent, type MemoryDraftCapture } from "@multica/core/projects";
import { useT } from "../../i18n";

export function ProjectMemory({ wsId, projectId, title }: { wsId: string; projectId: string; title: string }) {
  const { t } = useT("projects");
  const [open, setOpen] = useState(false);
  return <>
    <Button variant="ghost" size="sm" onClick={() => setOpen(true)}><BookOpen />{t(($) => $.memory.title)}</Button>
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="sm:max-w-4xl" aria-describedby="project-memory-description">
        <DialogHeader>
          <DialogTitle>{title} · {t(($) => $.memory.title)}</DialogTitle>
          <DialogDescription id="project-memory-description">{t(($) => $.memory.description)}</DialogDescription>
        </DialogHeader>
        {open && <ProjectMemoryEditor key={JSON.stringify([wsId, projectId])} wsId={wsId} projectId={projectId} />}
      </DialogContent>
    </Dialog>
  </>;
}

export function ProjectMemoryEditor({ wsId, projectId }: { wsId: string; projectId: string }) {
  const { t } = useT("projects");
  const queryClient = useQueryClient();
  const query = useQuery(memoryOptions(wsId, projectId));
  const [selected, setSelected] = useState("");
  const [search, setSearch] = useState("");
  const [savedPath, setSavedPath] = useState("");
  const [discardOpen, setDiscardOpen] = useState(false);
  const entries = useMemoryDraftStore((s) => s.draft.entries);
  const scopePrefix = JSON.stringify([wsId, projectId]).slice(0, -1) + ",";
  const draftPaths = Object.keys(entries).filter((k) => k.startsWith(scopePrefix)).flatMap((k) => {
    try { const parts: unknown = JSON.parse(k); return Array.isArray(parts) && typeof parts[2] === "string" ? [parts[2]] : []; } catch { return []; }
  });
  const paths = [...new Set([...Object.keys(query.data?.files ?? {}), ...draftPaths])].sort();
  const path = paths.includes(selected) ? selected : paths[0] ?? "";
  const key = memoryDraftKey(wsId, projectId, path);
  const draft = entries[key];
  const content = draft?.content ?? query.data?.files[path] ?? "";
  const save = useMutation({
    mutationFn: async (capture: MemoryDraftCapture) => {
      return saveMemory(wsId, projectId, path, capture.edit, (requestId) => updateCapturedMemoryDraft(capture, "submitted", requestId));
    },
    onSuccess: (snapshot, capture) => {
      if (!isMemoryDraftSessionCurrent(capture)) return;
      const cleared = updateCapturedMemoryDraft(capture, "saved");
      queryClient.setQueryData(memoryKey(wsId, projectId), snapshot);
      if (cleared) setSavedPath(path);
    },
    onError: (error, capture) => {
      if (error instanceof MemoryError && error.terminal) updateCapturedMemoryDraft(capture, "failed");
    },
    retry: false,
  });
  const dirty = draftPaths.length > 0;
  useEffect(() => {
    if (!dirty) return;
    const prevent = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", prevent);
    return () => window.removeEventListener("beforeunload", prevent);
  }, [dirty]);
  const problem = query.error ?? save.error;
  const kind = memoryProblem(problem);
  const messages = {
    permission: t(($) => $.memory.permission), uninitialized: t(($) => $.memory.uninitialized),
    conflict: t(($) => $.memory.conflict), unavailable: t(($) => $.memory.unavailable),
    unsupported: t(($) => $.memory.unsupported), invalid: t(($) => $.memory.invalid), error: t(($) => $.memory.error),
  };
  const conflict = !!draft && !!query.data && (draft.bindingRevision !== query.data.binding_revision || draft.contentRevision !== query.data.content_revision);
  return <div className="space-y-4">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <p className="text-caption text-muted-foreground" role="status">
        {query.isFetching ? t(($) => $.memory.loading) : dirty ? t(($) => $.memory.unsaved) : savedPath ? t(($) => $.memory.saved) : t(($) => $.memory.read_hint)}
      </p>
      <Button variant="outline" size="sm" disabled={query.isFetching || save.isPending} onClick={() => { save.reset(); void query.refetch(); }}>{t(($) => $.memory.refresh)}</Button>
    </div>
    {problem && <p role="alert" className="rounded-md border border-destructive/30 p-3 text-caption text-destructive">{messages[kind]}</p>}
    <AlertDialog open={discardOpen} onOpenChange={setDiscardOpen}>
      <AlertDialogContent><AlertDialogHeader><AlertDialogTitle>{t(($) => $.memory.discard)}</AlertDialogTitle><AlertDialogDescription>{t(($) => $.memory.discard_detail)}</AlertDialogDescription></AlertDialogHeader>
      <AlertDialogFooter><AlertDialogCancel>{t(($) => $.memory.cancel)}</AlertDialogCancel><AlertDialogAction onClick={() => { setMemoryDraft(key); save.reset(); setSavedPath(""); setDiscardOpen(false); }}>{t(($) => $.memory.discard)}</AlertDialogAction></AlertDialogFooter></AlertDialogContent>
    </AlertDialog>
    {!query.isPending && !query.error && query.data && <>
      {paths.length === 0 ? <p>{t(($) => $.memory.empty)}</p> : <div className="grid min-h-80 gap-4 sm:grid-cols-[13rem_minmax(0,1fr)]">
        <nav aria-label={t(($) => $.memory.files)} className="min-w-0 space-y-2">
          <Input aria-label={t(($) => $.memory.search)} placeholder={t(($) => $.memory.search)} value={search} onChange={(e) => setSearch(e.target.value)} />
          <div className="max-h-80 space-y-1 overflow-auto">
            {paths.filter((name) => name.toLocaleLowerCase().includes(search.toLocaleLowerCase())).map((name) => <button key={name} type="button" aria-current={path === name ? "page" : undefined} disabled={save.isPending} onClick={() => { setSelected(name); save.reset(); setSavedPath(""); }} className={`block w-full break-all rounded-md px-3 py-2 text-left text-caption ${path === name ? "bg-accent text-accent-foreground" : "hover:bg-accent/50"}`}>{name}{entries[memoryDraftKey(wsId, projectId, name)] ? " *" : ""}</button>)}
          </div>
        </nav>
        <div className="min-w-0 space-y-3">
          <label htmlFor="project-memory-content" className="block break-all text-caption font-medium">{path}</label>
          <Textarea id="project-memory-content" aria-label={t(($) => $.memory.content)} className="min-h-72 font-mono text-caption" spellCheck={false} value={content} disabled={save.isPending || !!draft?.requestId} onChange={(e) => {
            if (!query.data) return;
            setSavedPath(""); save.reset();
            setMemoryDraft(key, { content: e.target.value, bindingRevision: draft?.bindingRevision ?? query.data.binding_revision, contentRevision: draft?.contentRevision ?? query.data.content_revision });
          }} />
          {conflict && <div className="space-y-2 rounded-md border p-3">
            <p role="alert" className="text-caption">{t(($) => $.memory.conflict)}</p>
            <label className="block text-caption">{t(($) => $.memory.latest)}<Textarea readOnly value={query.data.files[path] ?? ""} className="mt-2 font-mono text-caption" /></label>
            <Button variant="outline" disabled={save.isPending || !!draft.requestId} onClick={() => { if (query.data) setMemoryDraft(key, { ...draft, bindingRevision: query.data.binding_revision, contentRevision: query.data.content_revision }); save.reset(); }}>{t(($) => $.memory.merge)}</Button>
          </div>}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-caption text-muted-foreground">{t(($) => $.memory.revision)} {query.data.content_revision}</span>
            <div className="flex gap-2">
            {draft && <Button variant="ghost" disabled={save.isPending || !!draft.requestId} onClick={() => setDiscardOpen(true)}>{t(($) => $.memory.discard)}</Button>}
            <Button disabled={!draft || save.isPending || query.isFetching || (conflict && !draft.requestId)} onClick={() => save.mutate(captureMemoryDraft(key))}>{save.isPending ? t(($) => $.memory.saving) : draft?.requestId ? t(($) => $.memory.verify) : t(($) => $.memory.save)}</Button>
            </div>
          </div>
        </div>
      </div>}
    </>}
  </div>;
}

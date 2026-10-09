"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FileCode, Folder, Loader2, Play, RefreshCw, Square } from "lucide-react";
import {
  buildToolTree, parseToolRun, toolsCatalogOptions, toolsRunsOptions, toolOutputOptions, toolsKeys,
  type LocalToolsBridge, type ToolFolder, type ToolRun,
} from "@multica/core/tools";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";
import { PAGE_GUTTER, PageHeader } from "../layout/page-header";

export function ToolsPage({ bridge }: { bridge?: LocalToolsBridge }) {
  const { t } = useT("tools");
  return <>
    <PageHeader><h1 className="text-label font-medium">{t(($) => $.title)}</h1></PageHeader>
    {bridge ? <LocalTools bridge={bridge} /> : <p className={`${PAGE_GUTTER} py-6 text-body text-muted-foreground`}>{t(($) => $.desktop_required)}</p>}
  </>;
}

function LocalTools({ bridge }: { bridge: LocalToolsBridge }) {
  const { t } = useT("tools");
  const client = useQueryClient();
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const catalog = useQuery(toolsCatalogOptions(bridge));
  const runs = useQuery(toolsRunsOptions(bridge));
  const tree = useMemo(() => buildToolTree(catalog.data?.scripts ?? [], search), [catalog.data, search]);
  const latest = useMemo(() => {
    const result = new Map<string, ToolRun>();
    for (const run of runs.data ?? []) if (!result.has(run.path)) result.set(run.path, run);
    return result;
  }, [runs.data]);
  const selectedRun = selected ? latest.get(selected) : undefined;
  const output = useQuery(toolOutputOptions(bridge, selectedRun?.id ?? ""));
  const run = useMutation({
    mutationFn: async (path: string) => parseToolRun(await bridge.run(path)),
    onSuccess: (result) => {
      client.setQueryData(toolsKeys.output(result.id), result);
      void client.invalidateQueries({ queryKey: toolsKeys.runs });
    },
  });
  const stop = useMutation({
    mutationFn: async (id: string) => parseToolRun(await bridge.stop(id)),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: toolsKeys.runs });
      if (selectedRun) void client.invalidateQueries({ queryKey: toolsKeys.output(selectedRun.id) });
    },
  });
  const error = catalog.error ?? runs.error ?? run.error ?? stop.error ?? output.error;
  const running = selectedRun?.status === "running";
  const exists = catalog.data?.scripts.some((script) => script.path === selected);
  const select = (path: string) => { setSelected(path); run.reset(); stop.reset(); };

  return <div className={`flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto py-4 ${PAGE_GUTTER}`}>
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="min-w-0">
        <p className="text-label font-medium">{t(($) => $.this_computer)}</p>
        {catalog.data && <code className="break-all text-caption text-muted-foreground">{catalog.data.root}</code>}
      </div>
      <Button size="sm" variant="outline" disabled={catalog.isFetching} onClick={() => { void catalog.refetch(); void runs.refetch(); }}>
        <RefreshCw className="size-4" />{t(($) => $.refresh)}
      </Button>
    </div>
    {error && <p role="alert" className="break-all text-body text-destructive">{error.message}</p>}
    {catalog.isLoading ? <p role="status" className="text-body">{t(($) => $.loading)}</p> :
      <div className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[minmax(16rem,2fr)_minmax(0,3fr)]">
        <section className="flex min-h-0 flex-col gap-3 rounded-lg border p-3" aria-label={t(($) => $.scripts)}>
          <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t(($) => $.search)} aria-label={t(($) => $.search)} />
          <div className="max-h-[60vh] overflow-auto lg:max-h-none">
            {tree.folders.length || tree.scripts.length ? <ScriptTree key={search ? "search" : "browse"} folder={tree} selected={selected} select={select} runs={latest} expand={!!search} /> :
              <p className="text-body text-muted-foreground">{t(($) => $.empty)}</p>}
          </div>
        </section>
        <section className="flex min-h-0 min-w-0 flex-col gap-3 rounded-lg border p-4" aria-label={t(($) => $.details)}>
          {selected ? <>
            <h2 className="break-all font-mono text-label font-medium">{selected}</h2>
            <div className="flex flex-wrap items-center gap-3">
              <RunStatus status={selectedRun?.status} />
              {selectedRun?.pid != null && <span className="text-caption text-muted-foreground">{t(($) => $.pid, { pid: selectedRun.pid })}</span>}
              {selectedRun?.exitCode != null && <span className="text-caption text-muted-foreground">{t(($) => $.exit_code, { code: selectedRun.exitCode })}</span>}
            </div>
            <div className="flex gap-2">
              <Button size="sm" disabled={!exists || running || run.isPending || runs.isError || runs.isLoading} aria-busy={run.isPending} onClick={() => run.mutate(selected)}>
                {running || run.isPending ? <Loader2 className="size-4 animate-spin" /> : <Play className="size-4" />}
                {running ? t(($) => $.running) : t(($) => $.run)}
              </Button>
              {running && <Button size="sm" variant="outline" disabled={stop.isPending} onClick={() => selectedRun && stop.mutate(selectedRun.id)}>
                <Square className="size-4" />{t(($) => $.stop)}
              </Button>}
            </div>
            <p className="text-caption text-muted-foreground">{t(($) => $.execution_hint)}</p>
            {output.data?.truncated && <p className="text-caption text-muted-foreground">{t(($) => $.truncated)}</p>}
            <pre aria-label={t(($) => $.output)} className="min-h-48 flex-1 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 font-mono text-caption">{output.data?.output || t(($) => $.no_output)}</pre>
          </> : <p className="text-body text-muted-foreground">{t(($) => $.select_script)}</p>}
        </section>
      </div>}
  </div>;
}

function RunStatus({ status }: { status?: ToolRun["status"] }) {
  const { t } = useT("tools");
  const label = status === "running" ? t(($) => $.running) : status === "succeeded" ? t(($) => $.succeeded) : status === "failed" ? t(($) => $.failed) : status === "stopped" ? t(($) => $.stopped) : t(($) => $.idle);
  return <span className={`shrink-0 text-caption ${status === "failed" ? "text-destructive" : status === "running" ? "text-brand" : "text-muted-foreground"}`}>{label}</span>;
}

function ScriptTree({ folder, selected, select, runs, expand }: {
  folder: ToolFolder; selected: string | null; select: (path: string) => void;
  runs: Map<string, ToolRun>; expand: boolean;
}) {
  const { t } = useT("tools");
  return <ul className="space-y-1">
    {folder.folders.map((child) => {
      const running = [...runs.values()].filter((run) => run.status === "running" && run.path.startsWith(`${child.path}/`)).length;
      return <li key={child.path}><details open={expand || undefined}>
        <summary className="cursor-pointer rounded-md px-2 py-1 text-label hover:bg-accent">
          <Folder className="mr-2 inline size-4" /><span>{child.name}</span>
          {running > 0 && <span className="ml-2 text-caption text-brand">{t(($) => $.running_count, { count: running })}</span>}
        </summary>
        <div className="ml-3 border-l pl-2"><ScriptTree folder={child} selected={selected} select={select} runs={runs} expand={expand} /></div>
      </details></li>;
    })}
    {folder.scripts.map((script) => <li key={script.path}>
      <button type="button" aria-pressed={selected === script.path} title={script.path} onClick={() => select(script.path)}
        className={`flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-label focus-visible:outline-2 focus-visible:outline-ring ${selected === script.path ? "bg-accent text-accent-foreground hover:bg-accent" : "hover:bg-muted"}`}>
        <FileCode className="size-4 shrink-0" /><span className="min-w-0 flex-1 break-all">{script.name}</span>{" "}
        {runs.has(script.path) && <RunStatus status={runs.get(script.path)?.status} />}
      </button>
    </li>)}
  </ul>;
}

"use client";

import { useEffect, useMemo, useState } from "react";
import { Loader2, RefreshCw } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { runtimeCLIRegistryOptions } from "@multica/core/clis";
import type { AgentRuntime } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { PAGE_GUTTER, PageHeader, PAGE_RAIL } from "../../layout/page-header";
import { useT } from "../../i18n";
import { CLIRunSection } from "./cli-run-section";

/**
 * CLI directory (TES-140).
 *
 * A registry entry names a command that is installed on a *machine* — the one
 * running the daemon — so the page is scoped by runtime, and it says which
 * machine it is talking to. Clicking Run executes on that machine, not in the
 * browser.
 *
 * Scheduling and model usage are explicitly not involved: unlike a skill, a
 * CLI run hands the command to no agent. That distinction is the reason this
 * surface exists next to Skills, so the page states it rather than leaving the
 * user to infer it.
 */
export function ClisPage() {
  const { t } = useT("clis");
  const wsId = useWorkspaceId();
  const currentUserId = useAuthStore((s) => s.user?.id);

  const { data: runtimes, isLoading: runtimesLoading } = useQuery(
    runtimeListOptions(wsId),
  );

  // Only the machine's owner can list or run its CLIs — the server enforces
  // this with 403, so the picker offers exactly the machines that will work
  // rather than letting the user discover the rule by hitting an error.
  const ownedRuntimes = useMemo(
    () => (runtimes ?? []).filter((rt) => rt.owner_id === currentUserId),
    [runtimes, currentUserId],
  );

  const [runtimeId, setRuntimeId] = useState<string | null>(null);
  useEffect(() => {
    if (runtimeId || ownedRuntimes.length === 0) return;
    // Prefer an online machine: an offline one cannot answer the registry
    // request, and landing on it would show a timeout the user cannot explain.
    const preferred = ownedRuntimes.find((rt) => rt.status === "online") ?? ownedRuntimes[0];
    if (!preferred) return;
    setRuntimeId(preferred.id);
  }, [ownedRuntimes, runtimeId]);

  const selected = ownedRuntimes.find((rt) => rt.id === runtimeId) ?? null;

  return (
    <>
      <PageHeader>
        <h1 className="text-sm font-medium">{t(($) => $.page.title)}</h1>
      </PageHeader>
      <div className={`flex-1 overflow-y-auto ${PAGE_GUTTER}`}>
        <div className={`${PAGE_RAIL} py-6`}>
          <p className="text-muted-foreground text-sm">
            {t(($) => $.page.tagline)}
          </p>

          {runtimesLoading ? (
            <Skeleton className="mt-4 h-9 w-72" />
          ) : ownedRuntimes.length === 0 ? (
            <p className="text-muted-foreground mt-6 text-sm">
              {t(($) => $.page.owner_only)}
            </p>
          ) : (
            <div className="mt-4 flex flex-wrap items-center gap-2">
              <Select
                items={ownedRuntimes.map((rt) => ({
                  value: rt.id,
                  label: runtimeLabel(rt),
                }))}
                value={runtimeId}
                onValueChange={(value) => value && setRuntimeId(value)}
              >
                <SelectTrigger className="w-72">
                  <SelectValue>
                    {selected ? runtimeLabel(selected) : null}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {ownedRuntimes.map((rt) => (
                    <SelectItem key={rt.id} value={rt.id}>
                      <span className="truncate">{runtimeLabel(rt)}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          {selected && <Registry runtimeId={selected.id} />}
        </div>
      </div>
    </>
  );
}

function runtimeLabel(rt: AgentRuntime): string {
  return runtimeDisplayLabel(rt);
}

function Registry({ runtimeId }: { runtimeId: string }) {
  const { t } = useT("clis");
  const { data, isLoading, isError, error, refetch, isFetching } = useQuery(
    runtimeCLIRegistryOptions(runtimeId),
  );

  if (isLoading) {
    return (
      <div className="mt-6 flex items-center gap-2 text-sm">
        <Loader2 className="size-4 animate-spin" />
      </div>
    );
  }

  if (isError) {
    return (
      <div className="mt-6 space-y-2">
        <p className="text-destructive text-sm">
          {t(($) => $.error.load_failed)}
        </p>
        <p className="text-muted-foreground text-xs">
          {error instanceof Error ? error.message : String(error)}
        </p>
        <Button size="sm" variant="outline" onClick={() => refetch()}>
          {t(($) => $.error.retry)}
        </Button>
      </div>
    );
  }

  const entries = data?.entries ?? [];

  return (
    <div className="mt-6 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        {data?.registryPath && (
          <p className="text-muted-foreground text-xs">
            {t(($) => $.page.registry_path)}:{" "}
            <code className="break-all">{data.registryPath}</code>
          </p>
        )}
        <Button
          size="sm"
          variant="ghost"
          onClick={() => refetch()}
          disabled={isFetching}
        >
          {isFetching ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <RefreshCw className="size-4" />
          )}
          {t(($) => $.page.reload)}
        </Button>
      </div>

      {entries.length === 0 ? (
        <div className="rounded-lg border p-4">
          <p className="text-sm font-medium">{t(($) => $.page.empty.title)}</p>
          <p className="text-muted-foreground mt-1 text-sm">
            {t(($) => $.page.empty.hint)}
          </p>
        </div>
      ) : (
        entries.map((entry) => (
          <CLIRunSection key={entry.key} runtimeId={runtimeId} entry={entry} />
        ))
      )}
    </div>
  );
}

"use client";

import { useEffect, useState } from "react";
import type { LocalDaemonStatus } from "./use-local-daemon-status";

// This structural bridge keeps Electron/preload types out of the shared view.
interface LocalIssueBridge {
  daemonAPI?: {
    getLocalIssueDefaultDirectory?(): Promise<string>;
    getStatus(): Promise<{ state: string; agents?: string[]; daemonId?: string }>;
    createLocalIssue?(request: { title: string; description: string; machine: "local"; directory: string; provider: string }): Promise<{ id: string; title: string }>;
  };
  desktopAPI?: { pickDirectory(defaultPath?: string): Promise<{ ok: boolean; path?: string }> };
}

function bridge(): LocalIssueBridge | undefined {
  return typeof window === "undefined" ? undefined : window as unknown as LocalIssueBridge;
}

export function useLocalIssueRunner(status: LocalDaemonStatus, initial: { directory?: string; provider?: string } = {}) {
  const [directory, setDirectory] = useState(initial.directory ?? "");
  const [provider, setProvider] = useState(initial.provider ?? "");
  const [error, setError] = useState("");
  const [defaultDirectory, setDefaultDirectory] = useState("");
  useEffect(() => {
    let live = true;
    void bridge()?.daemonAPI?.getLocalIssueDefaultDirectory?.().then(value => {
      if (live) setDefaultDirectory(value);
    }).catch(cause => { if (live) setError(String(cause)); });
    return () => { live = false; };
  }, [status.daemonId]);
  const effectiveDirectory = directory.trim() || defaultDirectory;
  const available = typeof bridge()?.daemonAPI?.createLocalIssue === "function";
  const providers = status.agents ?? [];
  const ready = available && status.running && !!effectiveDirectory && providers.length > 0 && (!provider || providers.includes(provider));

  return {
    directory, defaultDirectory, effectiveDirectory, setDirectory, provider, setProvider, providers, available, ready, error,
    getCurrentStatus: () => bridge()?.daemonAPI?.getStatus(),
    async browse() {
      setError("");
      try {
        const result = await bridge()?.desktopAPI?.pickDirectory(effectiveDirectory || undefined);
        if (result?.ok && result.path) setDirectory(result.path);
      } catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
    },
    async create(title: string, description: string) {
      const api = bridge()?.daemonAPI;
      if (!ready || !api?.createLocalIssue) throw new Error("Local daemon, CLI and working directory are required");
      const current = await api.getStatus();
      if (current.state !== "running" || !current.agents?.length || (provider && !current.agents.includes(provider))) {
        throw new Error("The local daemon or selected CLI is no longer available");
      }
      // There is deliberately no Center fallback on any failure here.
      return api.createLocalIssue({ title, description, machine: "local", directory: directory.trim(), provider });
    },
  };
}

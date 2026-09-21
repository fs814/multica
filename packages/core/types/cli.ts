/**
 * Machine-local CLI directory (TES-140).
 *
 * A "CLI entry" is a command registered on the machine that runs the daemon.
 * The panel lists entries and runs one on click. The request the client sends
 * names a registry key plus typed parameter values — never an executable, an
 * argv, or a shell string — so the whitelist cannot be widened from the UI.
 */

export type RuntimeCLIStatus =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "timeout";

/** One parameter slot an entry declares. */
export interface RuntimeCLIParamDescriptor {
  name: string;
  type: "enum" | "string";
  required: boolean;
  /** Present for `type: "enum"`. */
  values?: string[];
  /** Present for `type: "string"`. */
  max_len?: number;
}

/**
 * A registry entry as shown in the panel. Deliberately carries no executable
 * path, interpreter, content hash, or environment value — those stay on the
 * machine that owns the registry.
 */
export interface RuntimeCLISummary {
  key: string;
  label: string;
  description?: string;
  params?: RuntimeCLIParamDescriptor[];
  timeout_seconds: number;
  max_output_bytes: number;
  /**
   * False when the entry is declared but its pinned executable is missing or
   * fails its content-hash check. The entry is still listed so a broken
   * install is visible instead of silently absent.
   */
  available: boolean;
  unavailable_reason?: string;
}

export interface RuntimeCLIListRequest {
  id: string;
  runtime_id: string;
  status: RuntimeCLIStatus;
  clis?: RuntimeCLISummary[];
  /** Absolute path of the machine-local registry file the daemon read. */
  registry_path?: string;
  error?: string;
  created_at: string;
  updated_at: string;
}

export interface RuntimeCLIRegistryResult {
  entries: RuntimeCLISummary[];
  registryPath: string;
}

export interface CreateRuntimeCLIRunRequest {
  params?: Record<string, string>;
  /**
   * Advisory hint that sizes the server's running bound. The daemon clamps
   * against the registry, so this cannot extend the process's lifetime.
   */
  timeout_seconds?: number;
}

export interface RuntimeCLIRunRequest {
  id: string;
  runtime_id: string;
  cli_key: string;
  params?: Record<string, string>;
  status: RuntimeCLIStatus;

  output?: string;
  /** True when the process produced more than `max_output_bytes`. */
  truncated?: boolean;
  exit_code?: number;
  duration_ms?: number;
  /** Total bytes the process produced, not the bytes kept. */
  output_bytes?: number;
  /**
   * The command line that actually ran, reported by the daemon. The server
   * never knew it — without this the audit trail would record an intent
   * rather than an execution.
   */
  resolved_argv?: string[];
  error?: string;

  created_at: string;
  updated_at: string;
}

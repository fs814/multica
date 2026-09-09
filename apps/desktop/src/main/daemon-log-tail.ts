import { open } from "fs/promises";
import { StringDecoder } from "string_decoder";

const POLL_MS = 500;
const RETRY_MS = 2_000;
const MAX_RETRIES = 5;
const MAX_READ_BYTES = 32 * 1024;
const MAX_LINES = 200;

interface LogTailOptions {
  resolvePath: () => Promise<string | null>;
  isAlive: () => boolean;
  onLine: (line: string) => void;
}

// Each subscription owns its timer and cancellation state. Schedule only after
// I/O completes so slow disks cannot accumulate reads or out-of-order IPC.
export function startDaemonLogTail(options: LogTailOptions): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let position = 0;
  let identity: string | undefined;
  let decoder = new StringDecoder("utf8");
  let pending = "";
  let retries = 0;
  let discardPartial = false;

  const active = () => !stopped && options.isAlive();

  async function poll(): Promise<void> {
    let delay = POLL_MS;
    try {
      if (!active()) return;
      const path = await options.resolvePath();
      if (!active()) return;
      if (!path) {
        delay = RETRY_MS;
        if (++retries > MAX_RETRIES) stopped = true;
        return;
      }
      const handle = await open(path, "r");
      try {
        if (!active()) return;
        // Stat and read the same handle, including during file replacement.
        const stats = await handle.stat();
        if (!active()) return;
        const nextIdentity = `${path}:${stats.dev}:${stats.ino}:${stats.birthtimeMs}`;
        if (identity !== nextIdentity || stats.size < position) {
          position = 0;
          pending = "";
          decoder = new StringDecoder("utf8");
          discardPartial = false;
          identity = nextIdentity;
        }
        const from = Math.max(position, stats.size - MAX_READ_BYTES);
        if (from > position) {
          // The UI shows recent history only; full history stays on disk.
          pending = "";
          decoder = new StringDecoder("utf8");
          discardPartial = true;
        }
        const length = stats.size - from;
        if (length === 0) return;
        const buffer = Buffer.alloc(length);
        const { bytesRead } = await handle.read(buffer, 0, length, from);
        if (!active()) return;
        position = from + bytesRead;
        let text = decoder.write(buffer.subarray(0, bytesRead));
        if (discardPartial) {
          const newline = text.indexOf("\n");
          if (newline === -1) return;
          text = text.slice(newline + 1);
          discardPartial = false;
        }
        const lines = (pending + text).split("\n");
        pending = lines.pop() ?? "";
        if (pending.length > MAX_READ_BYTES) {
          pending = "";
          discardPartial = true;
        }
        for (const line of lines.filter(Boolean).slice(-MAX_LINES)) {
          if (!active()) return;
          options.onLine(line);
        }
        retries = 0;
      } finally {
        await handle.close();
      }
    } catch (error) {
      if (active()) {
        delay = RETRY_MS;
        if (++retries > MAX_RETRIES) {
          stopped = true;
          console.warn("[daemon] log tail stopped after repeated read failures:", error);
        }
      }
    } finally {
      if (active()) timer = setTimeout(() => void poll(), delay);
    }
  }

  void poll();
  return () => {
    stopped = true;
    if (timer !== undefined) clearTimeout(timer);
  };
}

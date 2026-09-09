import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { open } from "fs/promises";
import { startDaemonLogTail } from "./daemon-log-tail";

vi.mock("fs/promises", () => { const open = vi.fn(); return { open, default: { open } }; });

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("daemon log tail resource lifecycle", () => {
  let data: Buffer;
  let ino: number;
  let alive: boolean;
  let stop: (() => void) | undefined;
  const onLine = vi.fn();
  const close = vi.fn(async () => undefined);
  const read = vi.fn(async (buffer: Buffer, offset: number, length: number, position: number) => {
    const bytesRead = data.copy(buffer, offset, position, position + length);
    return { bytesRead, buffer };
  });

  function start(resolvePath = async () => "daemon.log") {
    stop = startDaemonLogTail({ resolvePath, isAlive: () => alive, onLine });
    return stop;
  }

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    data = Buffer.from("first\n");
    ino = 1;
    alive = true;
    vi.mocked(open).mockResolvedValue({
      stat: async () => ({ size: data.length, dev: 1, ino, birthtimeMs: ino }),
      read,
      close,
    } as unknown as Awaited<ReturnType<typeof open>>);
  });

  afterEach(() => {
    stop?.();
    vi.useRealTimers();
  });

  it("does not open a file when cancelled during profile resolution", async () => {
    const path = deferred<string>();
    start(() => path.promise)();
    path.resolve("daemon.log");
    await vi.advanceTimersByTimeAsync(10_000);
    expect(open).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("cancels the retry when the profile is not ready", async () => {
    const resolvePath = vi.fn(async (): Promise<string | null> => null);
    stop = startDaemonLogTail({ resolvePath, isAlive: () => alive, onLine });
    await vi.advanceTimersByTimeAsync(0);
    stop();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(resolvePath).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("closes an in-flight read without sending or scheduling after cancellation", async () => {
    const pending = deferred<{ bytesRead: number; buffer: Buffer }>();
    read.mockImplementationOnce(() => pending.promise);
    start();
    await vi.advanceTimersByTimeAsync(0);
    stop?.();
    pending.resolve({ bytesRead: 0, buffer: Buffer.alloc(0) });
    await vi.advanceTimersByTimeAsync(5_000);
    expect(close).toHaveBeenCalledTimes(1);
    expect(onLine).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("never overlaps reads on a slow disk", async () => {
    const pending = deferred<{ bytesRead: number; buffer: Buffer }>();
    read.mockImplementationOnce(() => pending.promise);
    start();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(open).toHaveBeenCalledTimes(1);
    expect(read).toHaveBeenCalledTimes(1);
    pending.resolve({ bytesRead: 0, buffer: Buffer.alloc(0) });
    await vi.advanceTimersByTimeAsync(0);
    expect(close).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(1);
  });

  it("bounds initial and burst reads and IPC while retaining the latest lines", async () => {
    data = Buffer.from(Array.from({ length: 10_000 }, (_, i) => `line-${i}\n`).join(""));
    start();
    await vi.advanceTimersByTimeAsync(0);
    expect(read.mock.calls[0]?.[2]).toBe(32 * 1024);
    expect(onLine).toHaveBeenCalledTimes(200);
    expect(onLine).toHaveBeenLastCalledWith("line-9999");
    onLine.mockClear();
    data = Buffer.concat([data, Buffer.from("x\n".repeat(100_000) + "latest\n")]);
    await vi.advanceTimersByTimeAsync(500);
    expect(read.mock.calls[1]?.[2]).toBe(32 * 1024);
    expect(onLine).toHaveBeenCalledTimes(200);
    expect(onLine).toHaveBeenLastCalledWith("latest");
  });

  it("preserves split UTF-8 and incomplete lines between reads", async () => {
    const bytes = Buffer.from("你好\n");
    data = bytes.subarray(0, 2);
    start();
    await vi.advanceTimersByTimeAsync(0);
    expect(onLine).not.toHaveBeenCalled();
    data = bytes;
    await vi.advanceTimersByTimeAsync(500);
    expect(onLine).toHaveBeenCalledExactlyOnceWith("你好");
  });

  it("discards oversized unfinished lines without retaining unbounded text", async () => {
    data = Buffer.from("x".repeat(20_000));
    start();
    await vi.advanceTimersByTimeAsync(0);
    data = Buffer.concat([data, Buffer.from("x".repeat(20_000))]);
    await vi.advanceTimersByTimeAsync(500);
    data = Buffer.concat([data, Buffer.from("tail\nnext\n")]);
    await vi.advanceTimersByTimeAsync(500);
    expect(onLine).toHaveBeenCalledExactlyOnceWith("next");
  });

  it("resets the cursor on truncation and replacement with a larger file", async () => {
    start();
    await vi.advanceTimersByTimeAsync(0);
    data = Buffer.from("a\n");
    await vi.advanceTimersByTimeAsync(500);
    expect(onLine).toHaveBeenLastCalledWith("a");
    ino = 2;
    data = Buffer.from("new beginning\nnew ending\n");
    await vi.advanceTimersByTimeAsync(500);
    expect(onLine.mock.calls.map(([line]) => line)).toEqual([
      "first", "a", "new beginning", "new ending",
    ]);
  });

  it("stops polling when the window is destroyed", async () => {
    start();
    await vi.advanceTimersByTimeAsync(0);
    alive = false;
    await vi.advanceTimersByTimeAsync(5_000);
    expect(open).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("closes the file after a failed read and retries without losing data", async () => {
    read.mockRejectedValueOnce(new Error("temporary read failure"));
    start();
    await vi.advanceTimersByTimeAsync(0);
    expect(close).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(close).toHaveBeenCalledTimes(2);
    expect(onLine).toHaveBeenCalledExactlyOnceWith("first");
  });
});

// Foreground supervisor for the center's API and Web only. No owner daemon.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export function preflight(repo) {
  const lock = path.join(repo, 'apps/web/.next/dev/lock');
  if (fs.existsSync(lock)) throw new Error(`Next development lock exists: ${lock}. Preserve the existing instance and use a separate source checkout, or stop it from its original terminal. Do not delete an active lock.`);
}

export async function supervise(specs, { ready, startupMs = 120000, graceMs = 5000, signal, completeOnExit = false, runMs } = {}) {
  const children = [];
  let finish;
  const stopped = new Promise(resolve => { finish = resolve; });
  const handlers = new Map(['SIGINT', 'SIGTERM'].map(signal => [signal, () => finish(signal === 'SIGINT' ? 130 : 143)]));
  for (const [signal, handler] of handlers) process.on(signal, handler);
  const onAbort = () => finish(signal.reason);
  signal?.addEventListener('abort', onAbort, { once: true });
  let timer;
  const runTimer = runMs ? setTimeout(() => finish(1), runMs) : undefined;
  let stopping = false;
  try {
    if (signal?.aborted) return signal.reason;
    for (const spec of specs) {
      const child = spawn(spec.command, spec.args, { cwd: spec.cwd, env: spec.env ?? process.env,
        stdio: 'inherit', detached: process.platform !== 'win32', shell: false });
      children.push(child);
      child.once('error', error => { console.error(`${spec.name} failed to start: ${error.message}`); finish(1); });
      child.once('exit', (code, signal) => {
        if (!stopping) {
          if (completeOnExit && code === 0) console.log(`${spec.name} completed successfully.`);
          else if (completeOnExit) console.error(`${spec.name} failed (${signal ?? code}); services not started.`);
          else console.error(`${spec.name} exited (${signal ?? code}); stopping this launch's services.`);
        }
        finish(completeOnExit && code === 0 ? 0 : (code || 1));
      });
      console.log(`Center child ${spec.name}: PID ${child.pid ?? 'unavailable'}`);
    }
    if (ready) {
      const deadline = Date.now() + startupMs;
      const check = async () => {
        if (stopping) return;
        try {
          if (await ready()) { console.log('Center ready: API and Web health checks passed.'); return; }
        } catch { /* Startup is not ready yet. */ }
        if (stopping) return;
        if (Date.now() >= deadline) { console.error('Center startup timed out.'); finish(1); }
        else timer = setTimeout(check, 250);
      };
      timer = setTimeout(check, 100);
    }
    return await stopped;
  } finally {
    stopping = true;
    clearTimeout(timer);
    clearTimeout(runTimer);
    // Unix groups are created above, never inherited from the invoking shell.
    // Windows uses the exact child PID and its descendants, never an image name.
    await Promise.all(children.map(async child => {
      if (!child.pid) return;
      if (process.platform === 'win32') {
        if (child.exitCode !== null || child.signalCode !== null) return;
        await new Promise(resolve => {
          const killer = spawn('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore', timeout: graceMs });
          killer.once('error', resolve); killer.once('exit', resolve);
        });
      } else {
        const signal = value => { try { process.kill(-child.pid, value); } catch (error) { if (error.code !== 'ESRCH') throw error; } };
        signal('SIGTERM');
        const deadline = Date.now() + graceMs;
        while (Date.now() < deadline) {
          try { process.kill(-child.pid, 0); } catch (error) { if (error.code === 'ESRCH') return; throw error; }
          await new Promise(resolve => setTimeout(resolve, 50));
        }
        signal('SIGKILL');
      }
    }));
    signal?.removeEventListener('abort', onAbort);
    for (const [signal, handler] of handlers) process.off(signal, handler);
  }
}

async function main() {
  const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
  preflight(repo);
  if (process.argv[2] === '--preflight') return;
  if (process.platform === 'win32' && process.argv[2] !== '--owned-job') {
    const owner = spawn('powershell.exe', ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-File',
      path.join(repo, 'scripts/center-job.ps1'), '-Node', process.execPath, '-Script', fileURLToPath(import.meta.url)], { stdio: 'inherit' });
    process.exitCode = await new Promise((resolve, reject) => {
      owner.once('error', reject); owner.once('exit', code => resolve(code ?? 1));
    });
    return;
  }
  // Compile first, then execute the binary directly: go run can leave the
  // actual API process behind after its wrapper exits on Windows.
  const controller = new AbortController();
  const handlers = new Map(['SIGINT', 'SIGTERM'].map(signal => [signal, () => controller.abort(signal === 'SIGINT' ? 130 : 143)]));
  for (const [signal, handler] of handlers) process.on(signal, handler);
  let dir;
  try {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), 'multica-center-'));
    const binary = path.join(dir, process.platform === 'win32' ? 'server.exe' : 'server');
    // Compilation owns a process group too, including compiler descendants.
    const code = await supervise([
      { name: 'Build', command: 'go', args: ['build', '-o', binary, './cmd/server'], cwd: path.join(repo, 'server') },
    ], { signal: controller.signal, completeOnExit: true, runMs: 180000 });
    if (controller.signal.aborted) { process.exitCode = controller.signal.reason; return; }
    if (code !== 0) throw new Error('Center API build failed or timed out; services not started.');
    preflight(repo);
    // Run Next directly to retain a single owned Windows child. The launcher
    // already exports the environment which Turbo formerly filtered.
    const next = path.join(repo, 'apps/web/node_modules/next/dist/bin/next');
    const api = `http://127.0.0.1:${process.env.PORT || 18081}/health`;
    const web = `http://127.0.0.1:${process.env.FRONTEND_PORT || 18080}/health`;
    process.exitCode = await supervise([
      { name: 'API', command: binary, args: [], cwd: path.join(process.env.MULTICA_CENTER_SOURCE_REPO || repo, 'server'),
        env: { ...process.env, MULTICA_CENTER_ONLY: '1' } },
      { name: 'Web', command: process.execPath, args: [next, 'dev', '--webpack', '--port', process.env.FRONTEND_PORT || '18080'], cwd: path.join(repo, 'apps/web') },
    ], { signal: controller.signal, ready: async () => {
      const responses = await Promise.all([api, web].map(url => fetch(url, { signal: AbortSignal.timeout(1500) })));
      return responses.every(response => response.ok);
    } });
  } finally {
    if (dir) fs.rmSync(dir, { recursive: true, force: true });
    for (const [signal, handler] of handlers) process.off(signal, handler);
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}

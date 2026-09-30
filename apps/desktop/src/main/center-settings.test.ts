// @vitest-environment node
import { describe, it, expect } from 'vitest';
import { mkdtemp, rm, writeFile, readFile } from 'fs/promises';
import { tmpdir } from 'os';
import { join } from 'path';
import { createServer } from 'http';
import { readCenterSettings, saveCenterSettings, saveSyncSourceSettings, testCenterConnection } from './center-settings';
import { centerRuntimeConfig, normalizeCenterUrl } from '../shared/center-settings';

describe('saved center preferences', () => {
  it('saves the HTTPS sync source independently and rejects HTTP and the peer origin', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'sync-source-settings-'));
    try {
      const mainFile = join(dir, 'center.json'), sourceFile = join(dir, 'center-sync-source.json');
      const main = { version: 1 as const, url: 'http://source.example:18080', profile: 'desktop-main' };
      await saveCenterSettings(main, mainFile);
      const saved = await saveSyncSourceSettings(' https://source.example:18082/ ', 'https://peer.example', sourceFile);
      expect(saved.url).toBe('https://source.example:18082');
      const before = await readFile(sourceFile, 'utf8');
      for (const url of ['http://source.example:18080', 'https://peer.example', 'https://user:secret@source.example', 'https://source.example/path']) {
        await expect(saveSyncSourceSettings(url, 'https://peer.example', sourceFile)).rejects.toThrow();
        expect(await readFile(sourceFile, 'utf8')).toBe(before);
      }
      expect(await readCenterSettings(mainFile)).toEqual(main);
      expect(before).not.toMatch(/token|password/i);
    } finally { await rm(dir, { recursive: true }); }
  });
  it('persists the transfer destination separately without changing the active server preference', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'center-transfer-settings-'));
    try {
      const activeFile = join(dir, 'center.json'), transferFile = join(dir, 'center-transfer.json');
      const active = { version: 1 as const, url: 'https://source.example', profile: 'desktop-services' };
      await saveCenterSettings(active, activeFile);
      await saveCenterSettings({ ...active, url: 'https://destination.example/' }, transferFile);
      expect(await readCenterSettings(activeFile)).toEqual(active);
      expect(await readCenterSettings(transferFile)).toEqual({ ...active, url: 'https://destination.example' });
      await expect(saveCenterSettings({ ...active, url: 'https://destination.example/path' }, transferFile)).rejects.toThrow();
      expect((await readCenterSettings(transferFile))?.url).toBe('https://destination.example');
    } finally { await rm(dir, { recursive: true }); }
  });
  it('persists independently of a connection and preserves the local IPv6 profile across address changes', async () => {
    const dir = await mkdtemp(join(tmpdir(), 'center-settings-')); const file = join(dir, 'center.json');
    try {
      expect(await readCenterSettings(file)).toBeNull();
      const first = { version: 1 as const, url: 'http://127.0.0.1:18080', profile: 'desktop-[--1]-8001' };
      await saveCenterSettings(first, file);
      expect(await readCenterSettings(file)).toEqual(first);
      await saveCenterSettings({ ...first, url: 'http://[::1]:18080/' }, file);
      expect(await readCenterSettings(file)).toEqual({ ...first, url: 'http://[::1]:18080' });
      const before = await readFile(file, 'utf8');
      await expect(saveCenterSettings({ ...first, url: 'http://host/path' }, file)).rejects.toThrow();
      expect(await readFile(file, 'utf8')).toBe(before);
      await writeFile(file, '{');
      await expect(readCenterSettings(file)).rejects.toThrow('save a valid');
    } finally { await rm(dir, { recursive: true }); }
  });
  it('derives API/Web/WS consistently and rejects unsafe or ambiguous addresses', () => {
    expect(centerRuntimeConfig('https://[fd00::20]:18080/')).toEqual({ schemaVersion: 1, apiUrl: 'https://[fd00::20]:18080', appUrl: 'https://[fd00::20]:18080', wsUrl: 'wss://[fd00::20]:18080/ws' });
    for (const url of ['file:///tmp/a', 'http://user:secret@host', 'http://host/path', 'http://host?x=1', 'http://0.0.0.0:1', 'http://[::]:1', 'http://host:0']) expect(() => normalizeCenterUrl(url)).toThrow();
  });
  it('tests reachability without credentials and recovers after failure without claiming a connection', async () => {
    let status = 401; const requests: string[] = [];
    const server = createServer((req, res) => { requests.push(req.url!); expect(req.headers.authorization).toBeUndefined(); res.writeHead(status, { 'content-type': 'application/json' }); res.end('{"error":"missing authorization"}'); });
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
    const url = 'http://127.0.0.1:' + (server.address() as import('net').AddressInfo).port;
    try {
      expect((await testCenterConnection(url)).reachable).toBe(true);
      status = 503; expect((await testCenterConnection(url)).reachable).toBe(false);
      status = 401; expect((await testCenterConnection(url)).message).toContain('does not sign in');
      expect(requests).toEqual(['/api/me', '/api/me', '/api/me']);
    } finally { await new Promise<void>(resolve => server.close(() => resolve())); }
    expect((await testCenterConnection(url)).reachable).toBe(false);
  });
});

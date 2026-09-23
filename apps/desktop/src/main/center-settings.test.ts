// @vitest-environment node
import { describe, it, expect } from 'vitest';
import { mkdtemp, rm, writeFile, readFile } from 'fs/promises';
import { tmpdir } from 'os';
import { join } from 'path';
import { createServer } from 'http';
import { readCenterSettings, saveCenterSettings, testCenterConnection } from './center-settings';
import { centerRuntimeConfig, normalizeCenterUrl } from '../shared/center-settings';

describe('saved center preferences', () => {
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

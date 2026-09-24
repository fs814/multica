import type { RuntimeConfig } from "./runtime-config";

export interface CenterSettings { version: 1; url: string; profile: string }
export interface CenterSettingsState { saved: CenterSettings | null; activeUrl: string; error?: string }
export interface CenterTestResult { reachable: boolean; message: string }

export function normalizeCenterUrl(value: unknown): string {
  if (typeof value !== "string") throw new Error("Enter an HTTP or HTTPS server address");
  const url = new URL(value.trim());
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password ||
      url.search || url.hash || (url.pathname !== '/' && url.pathname !== '') ||
      ['0.0.0.0', '[::]', '255.255.255.255'].includes(url.hostname) || url.port === '0') {
    throw new Error("Use an HTTP(S) origin, for example http://192.168.1.20:18080");
  }
  return url.origin;
}

export function parseCenterSettings(value: unknown): CenterSettings {
  if (!value || typeof value !== 'object') throw new Error('Invalid center settings');
  const v = value as Record<string, unknown>;
  if (v.version !== 1 || typeof v.profile !== 'string' ||
      !/^desktop-(?:[a-zA-Z0-9][a-zA-Z0-9_.-]{0,99}|\[[a-fA-F0-9.-]+\](?:-[0-9]+)?)(?![\s\S])/.test(v.profile)) {
    throw new Error('Invalid center settings version or local profile');
  }
  return { version: 1, url: normalizeCenterUrl(v.url), profile: v.profile };
}

export function centerRuntimeConfig(url: string): RuntimeConfig {
  const apiUrl = normalizeCenterUrl(url);
  return { schemaVersion: 1, apiUrl, appUrl: apiUrl, wsUrl: apiUrl.replace(/^http/, 'ws') + '/ws' };
}

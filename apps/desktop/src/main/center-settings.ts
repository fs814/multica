import { readFile, writeFile, mkdir, rename } from "fs/promises";
import { join, dirname } from "path";
import { homedir } from "os";
import { randomUUID } from "crypto";
import { normalizeCenterUrl, parseCenterSettings, type CenterSettings, type CenterTestResult } from "../shared/center-settings";

export function centerSettingsPath(): string { return join(homedir(), '.multica', 'center.json'); }
export async function readCenterSettings(file = centerSettingsPath()): Promise<CenterSettings | null> {
  try { return parseCenterSettings(JSON.parse(await readFile(file, 'utf8'))); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
    throw new Error('Cannot read saved center settings; save a valid server address in Settings');
  }
}
export async function saveCenterSettings(value: CenterSettings, file = centerSettingsPath()): Promise<CenterSettings> {
  const settings = parseCenterSettings(value);
  await mkdir(dirname(file), { recursive: true });
  const temporary = file + '.' + randomUUID() + '.tmp';
  await writeFile(temporary, JSON.stringify(settings, null, 2) + '\n', { mode: 0o600 });
  await rename(temporary, file);
  return settings;
}
export async function testCenterConnection(value: string): Promise<CenterTestResult> {
  try {
    const url = normalizeCenterUrl(value);
    // No credentials, no redirects, and no lifecycle side effects.
    const response = await fetch(url + '/api/me', { redirect: 'error', signal: AbortSignal.timeout(5000) });
    const body: unknown = await response.json();
    const apiResponse = body && typeof body === 'object' && ('error' in body || 'id' in body);
    if ([200, 401, 403].includes(response.status) && apiResponse) {
      return { reachable: true, message: 'Server API reachable. This test does not sign in or connect Desktop/daemon.' };
    }
    return { reachable: false, message: 'The address did not return a supported server API response.' };
  } catch { return { reachable: false, message: 'Connection failed. Check the address and whether the center is running.' }; }
}

import { readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { isAbsolute, join } from "node:path";
import { X509Certificate } from "node:crypto";
import { checkServerIdentity } from "node:tls";
import { isIP } from "node:net";
import { Agent } from "undici";
import { normalizeCenterUrl } from "../shared/center-settings";

function readSmallFile(file: string): string {
  const info = statSync(file);
  if (!info.isFile() || info.size > 64 * 1024) throw new Error("Invalid center certificate file size");
  return readFileSync(file, "utf8");
}

interface TrustedCenter { origin: string; pem: string; fingerprint: string }

export function readTrustedCenters(file: string): TrustedCenter[] {
  let raw: unknown;
  try { raw = JSON.parse(readSmallFile(file)); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return [];
    throw new Error("Cannot read center certificate configuration");
  }
  if (!raw || typeof raw !== "object" || !("version" in raw) || raw.version !== 1 ||
      !("centers" in raw) || !Array.isArray(raw.centers) || raw.centers.length > 16) {
    throw new Error("Invalid center certificate configuration");
  }
  const seen = new Set<string>();
  return raw.centers.map((entry: unknown) => {
    if (!entry || typeof entry !== "object" || !("origin" in entry) || typeof entry.origin !== "string" ||
        !("certificateFile" in entry) || typeof entry.certificateFile !== "string" || !isAbsolute(entry.certificateFile) ||
        !("fingerprint256" in entry) || typeof entry.fingerprint256 !== "string" || !/^(?:[A-F0-9]{2}:){31}[A-F0-9]{2}$/.test(entry.fingerprint256)) {
      throw new Error("Invalid trusted center entry");
    }
    const origin = normalizeCenterUrl(entry.origin);
    if (origin !== entry.origin || !origin.startsWith("https:") || seen.has(origin)) throw new Error("Invalid or duplicate trusted center origin");
    seen.add(origin);
    const pem = readSmallFile(entry.certificateFile);
    if (!/^\s*-----BEGIN CERTIFICATE-----[A-Za-z0-9+/=\s]+-----END CERTIFICATE-----\s*$/.test(pem)) {
      throw new Error("Supply one public certificate only, never a private key");
    }
    const cert = new X509Certificate(pem);
    const host = new URL(origin).hostname.replace(/^\[|\]$/g, "");
    if (cert.fingerprint256 !== entry.fingerprint256 ||
        !(isIP(host) ? cert.checkIP(host) : cert.checkHost(host, { subject: "never" })) ||
        Date.now() < Date.parse(cert.validFrom) || Date.now() >= Date.parse(cert.validTo)) {
      throw new Error("Center certificate fingerprint, hostname or validity does not match");
    }
    return { origin, pem, fingerprint: cert.fingerprint256 };
  });
}

/** Operator-selected leaf trust for native center requests only. Never changes
 * global fetch, OS/browser trust, redirect policy or sync authorization.
 * Configuration is loaded once; restart Desktop after changing or revoking it.
 */
export function createCenterCertificateFetch(
  file = join(homedir(), ".multica", "center-certificates.json"),
  fetcher: typeof fetch = fetch,
) {
  let agents: Map<string, Agent> | undefined;
  const request: typeof fetch = async (input, init) => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    if (!agents) {
      const trusted = readTrustedCenters(file);
      agents = new Map(trusted.map(({ origin, pem, fingerprint }) => [origin, new Agent({
        connect: {
          ca: pem, allowPartialTrustChain: true, rejectUnauthorized: true,
          // BoringSSL needs explicit leaf-as-anchor support. TLS still verifies
          // signatures/expiry; also pin the exact peer and check its hostname.
          checkServerIdentity(host, cert) {
            const error = checkServerIdentity(host, cert);
            if (error) return error;
            if (!cert.raw || new X509Certificate(cert.raw).fingerprint256 !== fingerprint) return new Error("Center certificate pin mismatch");
            return undefined;
          },
        },
      })]));
    }
    const dispatcher = agents.get(url.origin);
    if (!dispatcher) return fetcher(input, init);
    const options: RequestInit & { dispatcher: Agent } = { ...init, dispatcher, redirect: "error" };
    return fetcher(input, options);
  };
  return { fetch: request, close: async () => { await Promise.all([...agents?.values() ?? []].map(agent => agent.close())); agents = undefined; } };
}

export const centerCertificateFetch = createCenterCertificateFetch().fetch;

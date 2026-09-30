// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { X509Certificate } from "node:crypto";
import { createServer } from "node:https";
import type { AddressInfo } from "node:net";
import { createCenterCertificateFetch, readTrustedCenters } from "./center-certificate-fetch";

const cleanup: (() => Promise<unknown> | void)[] = [];
afterEach(async () => { for (const fn of cleanup.splice(0).reverse()) await fn(); });

function fixture() {
  const dir = mkdtempSync(join(tmpdir(), "multica-cert-test-"));
  cleanup.push(() => rmSync(dir, { recursive: true, force: true }));
  const cert = join(dir, "cert.pem"), key = join(dir, "key.pem"), config = join(dir, "trust.json");
  execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
    "-subj", "/CN=fixture-only", "-addext", "subjectAltName=IP:127.0.0.1", "-addext", "basicConstraints=critical,CA:FALSE",
    "-keyout", key, "-out", cert], { stdio: "pipe" });
  const fingerprint256 = new X509Certificate(readFileSync(cert)).fingerprint256;
  const save = (origin: string, extra = {}) => writeFileSync(config, JSON.stringify({ version: 1, centers: [{ origin, certificateFile: cert, fingerprint256, ...extra }] }));
  return { dir, cert, key, config, save };
}

async function endpoint(f: ReturnType<typeof fixture>) {
  const requests: string[] = [];
  const server = createServer({ cert: readFileSync(f.cert), key: readFileSync(f.key) }, (req, res) => {
    requests.push(req.url!);
    if (req.url === "/redirect") { res.writeHead(302, { location: "/must-not-follow" }); res.end(); return; }
    res.writeHead(401, { "content-type": "application/json" }); res.end('{"error":"sign in required"}');
  });
  await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
  cleanup.push(() => new Promise<void>(resolve => { server.closeAllConnections(); server.close(() => resolve()); }));
  return { origin: `https://127.0.0.1:${(server.address() as AddressInfo).port}`, requests };
}

describe("explicit native center certificate trust", () => {
  it("verifies the selected self-signed leaf without bypassing authentication, redirects or cancellation", async () => {
    const f = fixture(), server = await endpoint(f);
    f.save(server.origin);
    const client = createCenterCertificateFetch(f.config); cleanup.push(client.close);
    const response = await client.fetch(server.origin + "/api/me");
    expect(response.status).toBe(401);
    expect(await response.json()).toEqual({ error: "sign in required" });
    await expect(client.fetch(server.origin + "/redirect", { redirect: "follow" })).rejects.toThrow();
    await expect(client.fetch(server.origin + "/aborted", { signal: AbortSignal.abort() })).rejects.toThrow();
    expect(server.requests).toEqual(["/api/me", "/redirect"]);
  });

  it("does not apply trusted certificates to another origin", async () => {
    const f = fixture(), first = await endpoint(f), second = await endpoint(f);
    f.save(first.origin);
    const client = createCenterCertificateFetch(f.config); cleanup.push(client.close);
    await expect(client.fetch(second.origin + "/api/me")).rejects.toThrow();
    expect(second.requests).toEqual([]);
  });

  it("rejects a different certificate served at the trusted address", async () => {
    const trusted = fixture(), different = fixture(), server = await endpoint(different);
    trusted.save(server.origin);
    const client = createCenterCertificateFetch(trusted.config); cleanup.push(client.close);
    await expect(client.fetch(server.origin + "/api/me")).rejects.toThrow();
    expect(server.requests).toEqual([]);
  });

  it("rejects mismatched files, names, private keys and unsafe origins", () => {
    const f = fixture();
    f.save("https://127.0.0.1:18082");
    expect(readTrustedCenters(f.config)).toHaveLength(1);
    for (const origin of ["http://127.0.0.1:18082", "https://127.0.0.1:18082/", "https://wrong.example:18082", "https://user@127.0.0.1:18082"]) {
      f.save(origin); expect(() => readTrustedCenters(f.config)).toThrow();
    }
    for (const extra of [{ fingerprint256: "00:".repeat(31) + "00" }, { certificateFile: f.key }, { certificateFile: "relative.pem" }]) {
      f.save("https://127.0.0.1:18082", extra); expect(() => readTrustedCenters(f.config)).toThrow();
    }
    writeFileSync(f.config, "{"); expect(() => readTrustedCenters(f.config)).toThrow();
    writeFileSync(f.config, JSON.stringify({ version: 1, centers: Array(17).fill({}) })); expect(() => readTrustedCenters(f.config)).toThrow();
  });

  it("keeps normal fetch behavior when no trust configuration exists", async () => {
    const f = fixture();
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response("ok"));
    const client = createCenterCertificateFetch(f.config, fetcher); cleanup.push(client.close);
    const options = { redirect: "error" as const };
    await client.fetch("https://public.example/api/me", options);
    expect(fetcher).toHaveBeenCalledExactlyOnceWith("https://public.example/api/me", options);
  });

  it("rejects expired and not-yet-valid certificate configuration", () => {
    const f = fixture();
    f.save("https://127.0.0.1:18082");
    const cert = new X509Certificate(readFileSync(f.cert));
    const now = vi.spyOn(Date, "now");
    try {
      now.mockReturnValue(Date.parse(cert.validFrom) - 1);
      expect(() => readTrustedCenters(f.config)).toThrow(/validity/);
      now.mockReturnValue(Date.parse(cert.validTo) + 1);
      expect(() => readTrustedCenters(f.config)).toThrow(/validity/);
    } finally { now.mockRestore(); }
  });
});

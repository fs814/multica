// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseRecoveryRequest } from "./center-recovery";

describe("password-only recovery IPC", () => {
  const request = { password: "portable backup password", session: { origin: "https://CENTER.example/", token: "normal-login-session" } };
  it("normalizes the login origin without changing the password or session", () => {
    expect(parseRecoveryRequest(request)).toEqual({ ...request, session: { ...request.session, origin: "https://center.example" } });
  });
  it("rejects missing credentials, invalid origins and invalid passwords", () => {
    for (const value of [null, {}, { ...request, session: null }, { ...request, password: "short" }, { ...request, password: "x".repeat(1025) },
      ...["", "with spaces", "line\nbreak", "x".repeat(8193)].map(token => ({ ...request, session: { ...request.session, token } })),
      ...["file:///tmp", "https://user:password@center.example", "https://center.example/path"].map(origin => ({ ...request, session: { ...request.session, origin } }))]) {
      expect(() => parseRecoveryRequest(value)).toThrow();
    }
  });
});

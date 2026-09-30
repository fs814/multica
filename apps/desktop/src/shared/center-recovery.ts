import { normalizeCenterUrl } from "./center-settings";

export interface CenterTransferRequest {
  targetUrl: string;
  sourceRecoveryToken: string;
  targetRecoveryToken: string;
}
export function parseTransferRequest(value: unknown): CenterTransferRequest {
  if (!value || typeof value !== "object") throw new Error("Invalid transfer request");
  const request = value as Record<string, unknown>;
  const token = (raw: unknown): string => {
    if (typeof raw !== "string" || raw.length > 4096 || /[\r\n]/.test(raw)) throw new Error("Invalid recovery token");
    const trimmed = raw.trim();
    if (trimmed && (trimmed.length < 32 || /\s/.test(trimmed))) throw new Error("Recovery token must contain at least 32 characters and no whitespace");
    return trimmed;
  };
  return { targetUrl: normalizeCenterUrl(request.targetUrl), sourceRecoveryToken: token(request.sourceRecoveryToken), targetRecoveryToken: token(request.targetRecoveryToken) };
}

export interface CenterRecoveryRequest {
  password: string;
  session: { origin: string; token: string };
}
export interface CenterRecoveryResult { cancelled: boolean; filePath?: string; jobId?: string }
export interface CenterRecoveryProgress { state: string; message?: string }
export function parseRecoveryRequest(value: unknown): CenterRecoveryRequest {
  if (!value || typeof value !== "object") throw new Error("Invalid recovery request");
  const request = value as Record<string, unknown>;
  const session = request.session;
  if (typeof request.password !== "string" || request.password.length < 12 || request.password.length > 1024 ||
      !session || typeof session !== "object" || !("origin" in session) || typeof session.origin !== "string" ||
      !("token" in session) || typeof session.token !== "string" || !session.token || session.token.length > 8192 || /\s/.test(session.token)) {
    throw new Error("Sign in to the center and use a backup password of at least 12 characters");
  }
  return { password: request.password, session: { origin: normalizeCenterUrl(session.origin), token: session.token } };
}

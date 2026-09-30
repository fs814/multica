export interface CenterRecoveryRequest {
  password: string;
  recoveryToken: string;
}
export interface CenterRecoveryResult { cancelled: boolean; filePath?: string; jobId?: string }
export interface CenterRecoveryProgress { state: string; message?: string }
export function parseRecoveryRequest(value: unknown): CenterRecoveryRequest {
  if (!value || typeof value !== "object") throw new Error("Invalid recovery request");
  const request = value as Record<string, unknown>;
  if (typeof request.password !== "string" || request.password.length < 12 || request.password.length > 1024 ||
      typeof request.recoveryToken !== "string" || request.recoveryToken.length > 4096 || /[\r\n]/.test(request.recoveryToken)) {
    throw new Error("Use a backup password of at least 12 characters and a valid recovery token");
  }
  return { password: request.password, recoveryToken: request.recoveryToken.trim() };
}

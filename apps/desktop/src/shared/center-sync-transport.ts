export interface CenterSyncTransportRequest {
  id: string;
  origin: string;
  path: string;
  body?: string;
  token?: string;
}

export type CenterSyncTransportResult =
  | { ok: true; status: number; body: string }
  | { ok: false; reason: "network" | "timeout" | "aborted" | "invalid_request" | "capacity" };

export interface CenterSyncTransport {
  syncRequest(request: CenterSyncTransportRequest): Promise<CenterSyncTransportResult>;
  cancelSyncRequest(id: string): Promise<void>;
}

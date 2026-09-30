import { z } from "zod";
import { parseWithFallback } from "./schema";

const statusSchema = z.object({
  job_id: z.string().regex(/^[a-f0-9]{32}$/),
  state: z.string(),
  center_id: z.string().min(1).max(512),
  message: z.string().max(2048).optional().default(""),
}).transform(value => ({ jobId: value.job_id, state: value.state, centerId: value.center_id, message: value.message }));
export interface CenterImportStatus { jobId: string; state: string; centerId: string; message: string }
export function parseCenterImportStatus(data: unknown): CenterImportStatus | null {
  return parseWithFallback<CenterImportStatus | null>(data, statusSchema, null, { endpoint: "center/recovery/import-status" });
}

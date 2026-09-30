import { queryOptions } from "@tanstack/react-query";
import { z } from "zod";
import { getApi } from "./index";
import { parseWithFallback } from "./schema";
import type { CenterSyncSession } from "./center-sync-session";

const workspacesSchema = z.array(z.object({
  id: z.string().uuid(),
  name: z.string().min(1),
  slug: z.string().optional(),
}));

/** A separate connection-scoped list must not reuse another center's cache. */
export function centerSyncWorkspaceListOptions(origin: string, connection: string, session?: CenterSyncSession) {
  return queryOptions({
    queryKey: ["workspaces", "center-sync", origin, connection, session ? "separate" : "primary"],
    gcTime: 0,
    retry: false,
    queryFn: async ({ signal }) => {
      if (session) {
        const user = session.currentUser;
        if (!user || session.origin !== origin) throw new Error("Source connection changed; reconnect for sync");
        const raw = await session.listWorkspaces(signal);
        if (session.currentUser !== user) throw new Error("Source connection changed; reconnect for sync");
        const result = parseWithFallback<z.infer<typeof workspacesSchema> | null>(raw, workspacesSchema, null, { endpoint: "center-sync/workspaces" });
        if (!result) throw new Error("Invalid source workspace list");
        return result;
      }
      const api = getApi();
      const token = api.getToken();
      const checkConnection = () => {
        if (getApi() !== api || api.getToken() !== token || api.getBaseUrl().replace(/\/$/, "") !== origin) {
          throw new Error("Source connection changed; reopen sync settings");
        }
      };
      checkConnection();
      const raw: unknown = await api.listWorkspaces();
      checkConnection();
      const result = parseWithFallback<z.infer<typeof workspacesSchema> | null>(
        raw, workspacesSchema, null, { endpoint: "center-sync/workspaces" },
      );
      if (!result) throw new Error("Invalid source workspace list");
      return result;
    },
  });
}

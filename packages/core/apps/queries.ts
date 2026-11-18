import { queryOptions } from "@tanstack/react-query";
import { parseInstalledApps, type LocalAppsBridge } from "./models";

export const appsKeys = { list: ["local-apps", "list"] as const };

export function installedAppsOptions(bridge: LocalAppsBridge) {
  return queryOptions({ queryKey: appsKeys.list, queryFn: async () => parseInstalledApps(await bridge.list()), staleTime: 5 * 60_000 });
}

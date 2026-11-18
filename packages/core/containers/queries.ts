import { queryOptions } from "@tanstack/react-query";
import { parseEngineStatus, parseResourceList, type EngineKind, type LocalContainersBridge, type ResourceKind } from "./models";

export const containersKeys = {
  all: ["local-containers"] as const,
  status: ["local-containers", "status"] as const,
  list: (engine: EngineKind | null, resource: ResourceKind) => ["local-containers", "list", engine, resource] as const,
};

export function engineStatusOptions(bridge: LocalContainersBridge) {
  return queryOptions({ queryKey: containersKeys.status, queryFn: async () => parseEngineStatus(await bridge.status()), staleTime: 30_000 });
}

/** Containers refresh every few seconds while the page is open, so state changes made elsewhere show up. */
export function resourceListOptions<K extends ResourceKind>(bridge: LocalContainersBridge, engine: EngineKind | null, resource: K, enabled: boolean) {
  return queryOptions({
    queryKey: containersKeys.list(engine, resource),
    queryFn: async () => parseResourceList(resource, await bridge.list(resource)),
    enabled,
    refetchInterval: resource === "containers" ? 5_000 : false,
    staleTime: 2_000,
  });
}

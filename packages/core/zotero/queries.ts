import { queryOptions } from "@tanstack/react-query";
import { parseZoteroCatalog, parseZoteroCollections, parseZoteroStatus, type LocalZoteroBridge } from "./models";

export const zoteroKeys = {
  catalog: ["local-zotero", "catalog"] as const,
  status: ["local-zotero", "status"] as const,
  collections: ["local-zotero", "collections"] as const,
};

export function zoteroCatalogOptions(bridge: LocalZoteroBridge) {
  return queryOptions({ queryKey: zoteroKeys.catalog, queryFn: async () => parseZoteroCatalog(await bridge.list()), staleTime: 60_000 });
}

export function zoteroStatusOptions(bridge: LocalZoteroBridge) {
  return queryOptions({ queryKey: zoteroKeys.status, queryFn: async () => parseZoteroStatus(await bridge.status()), staleTime: 10_000 });
}

export function zoteroCollectionsOptions(bridge: LocalZoteroBridge, enabled: boolean) {
  return queryOptions({
    queryKey: zoteroKeys.collections,
    queryFn: async () => parseZoteroCollections(await bridge.collections()),
    enabled,
    staleTime: 30_000,
  });
}

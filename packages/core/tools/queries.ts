import { queryOptions } from "@tanstack/react-query";
import { parseToolCatalog, parseToolRun, parseToolRuns, type LocalToolsBridge } from "./models";

export const toolsKeys = {
  catalog: ["local-tools", "catalog"] as const,
  runs: ["local-tools", "runs"] as const,
  output: (id: string) => ["local-tools", "output", id] as const,
};

export function toolsCatalogOptions(bridge: LocalToolsBridge) {
  return queryOptions({ queryKey: toolsKeys.catalog, queryFn: async () => parseToolCatalog(await bridge.catalog()), staleTime: 30_000 });
}

export function toolsRunsOptions(bridge: LocalToolsBridge) {
  return queryOptions({ queryKey: toolsKeys.runs, queryFn: async () => parseToolRuns(await bridge.runs()), refetchInterval: 1000 });
}

export function toolOutputOptions(bridge: LocalToolsBridge, id: string) {
  return queryOptions({
    queryKey: toolsKeys.output(id), queryFn: async () => parseToolRun(await bridge.output(id)), enabled: !!id,
    refetchInterval: (query) => query.state.data?.status === "running" ? 1000 : false,
  });
}

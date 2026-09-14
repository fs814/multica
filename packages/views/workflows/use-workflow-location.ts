"use client";

import { useNavigation } from "../navigation";

/** List state belongs to the route so refresh, history and desktop tabs agree. */
export function useWorkflowLocation() {
  const navigation = useNavigation();
  const params = navigation.searchParams ?? new URLSearchParams();
  return {
    params,
    current: navigation.pathname + (params.toString() ? `?${params}` : ""),
    update(values: Record<string, string | number | null>, replace = false) {
      const next = new URLSearchParams(params);
      for (const [key, value] of Object.entries(values)) {
        if (value === null || value === "" || value === 0) next.delete(key);
        else next.set(key, String(value));
      }
      const query = next.toString();
      const path =
        navigation.pathname +
        (query ? `?${query}` : "") +
        (navigation.hash ?? "");
      if (replace) navigation.replace(path);
      else navigation.push(path);
    },
  };
}

export {
  workflowListOffset,
  workflowReturnPath,
} from "@multica/core/workflows";

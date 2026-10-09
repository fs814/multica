export interface WebLink {
  id: string;
  name: string;
  url: string;
}

/** Accept web pages only; never persist credentials in a bookmark URL. */
export function normalizeWebUrl(value: unknown): string | null {
  if (typeof value !== "string" || !value.trim()) return null;
  try {
    const url = new URL(value.trim());
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) return null;
    return url.href;
  } catch {
    return null;
  }
}

export function parseWebLinks(value: unknown): WebLink[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  return value.flatMap((entry: unknown) => {
    if (!entry || typeof entry !== "object") return [];
    const item = entry as Record<string, unknown>;
    const url = normalizeWebUrl(item.url);
    if (typeof item.id !== "string" || !item.id || seen.has(item.id) ||
        typeof item.name !== "string" || !item.name.trim() || !url) return [];
    seen.add(item.id);
    return [{ id: item.id, name: item.name.trim(), url }];
  });
}

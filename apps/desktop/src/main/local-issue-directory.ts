import { mkdir } from "fs/promises";
import { join } from "path";

export function defaultLocalIssueDirectory(profileDirectory: string): string {
  return join(profileDirectory, "local-workspace");
}

/** Only provision the managed default; never create a mistyped user path. */
export async function resolveLocalIssueDirectory(directory: string, profileDirectory: string): Promise<string> {
  const fallback = defaultLocalIssueDirectory(profileDirectory);
  const selected = directory.trim() || fallback;
  if (selected === fallback) await mkdir(fallback, { recursive: true, mode: 0o700 });
  return selected;
}

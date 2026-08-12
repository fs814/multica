/**
 * Reading a submission's `artifact` bag.
 *
 * The artifact is deliberately an open `Record<string, unknown>`: its shape is a
 * per-node contract (an analysis artifact and a code-change artifact carry
 * different keys), not a per-engine one, so the schema keeps it open rather than
 * pinning today's `{type, summary, references}`.
 *
 * That openness has to be paid for exactly here. A view that reached in for
 * `artifact.summary` and rendered it directly would print `[object Object]` the
 * first time a node contract made `summary` structured - and the artifact
 * summary is the line a reviewer reads to decide whether to accept, so a broken
 * one is worse than none.
 */

/**
 * The artifact's human summary, or `""` when it has none this build can read.
 *
 * Only a *string* summary is accepted. A number or an object is not coerced:
 * `String({})` is `[object Object]`, and a caller cannot tell that apart from a
 * legitimate summary, so it would be rendered as prose.
 */
export function artifactSummary(artifact: Record<string, unknown>): string {
  const summary = artifact.summary;
  return typeof summary === "string" ? summary.trim() : "";
}

/** The artifact's `type` discriminator, or `""` when absent/unreadable. */
export function artifactType(artifact: Record<string, unknown>): string {
  const type = artifact.type;
  return typeof type === "string" ? type.trim() : "";
}

/**
 * The artifact's references, as strings.
 *
 * Non-string entries are dropped rather than stringified - a reference is
 * something a reader is meant to be able to follow (a path, a URL, an issue
 * identifier), and a rendered `[object Object]` in that list is a dead end
 * dressed up as a link target. Dropping keeps the list honest; the raw artifact
 * is still visible in the JSON detail for anyone who needs the rest.
 */
export function artifactReferences(
  artifact: Record<string, unknown>,
): string[] {
  const refs = artifact.references;
  if (!Array.isArray(refs)) return [];
  return refs.filter((ref): ref is string => typeof ref === "string");
}

/**
 * True when the artifact carries nothing beyond what the trace already renders
 * separately (`type`, `summary`, `references`).
 *
 * Used to decide whether the raw-JSON fallback is worth showing. A node
 * contract that adds its own keys is exactly the case where an operator needs
 * to see them, and a node that adds none should not get an empty `{}` block.
 */
export function artifactHasExtraKeys(
  artifact: Record<string, unknown>,
): boolean {
  const known = new Set(["type", "summary", "references"]);
  return Object.keys(artifact).some((key) => !known.has(key));
}

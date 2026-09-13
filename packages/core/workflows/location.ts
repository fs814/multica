export function workflowListOffset(value: string | null) {
  const offset = Number(value);
  return Number.isSafeInteger(offset) && offset >= 0
    ? Math.floor(offset / 30) * 30
    : 0;
}

export function workflowReturnPath(
  value: string | null,
  fallback: string,
  allowed: string[],
) {
  if (!value || !allowed.includes(value.split(/[?#]/)[0] ?? ""))
    return fallback;
  return value;
}

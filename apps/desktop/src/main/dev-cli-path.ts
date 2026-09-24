import { isAbsolute } from "path";

/** A dev launcher override must never influence the packaged application. */
export function desktopDevCliPath(isPackaged: boolean, value: string | undefined): string | null {
  if (isPackaged || !value) return null;
  if (!isAbsolute(value)) throw new Error("Desktop development CLI path must be absolute");
  return value;
}

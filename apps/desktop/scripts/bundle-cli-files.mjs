import { copyFile, rename, rm } from "node:fs/promises";
import { randomUUID } from "node:crypto";

export function shouldKeepBundledCli(goAvailable, destinationExists) {
  return !goAvailable && destinationExists;
}

// Stage next to the destination, then rename. A locked Windows executable
// leaves the working bundle intact; never recursively delete resources/bin.
export async function replaceBundledCli(source, destination, {
  allowLockedDestination = false, platform = process.platform,
  copy = copyFile, move = rename, remove = rm, warn = console.warn,
} = {}) {
  const staged = `${destination}.${randomUUID()}.tmp`;
  try {
    await copy(source, staged);
    try {
      await move(staged, destination);
    } catch (error) {
      if (platform !== "win32" || !["EPERM", "EACCES", "EBUSY"].includes(error.code)) throw error;
      const message = `[bundle-cli] Cannot replace ${destination}: it may be in use or not writable. ` +
        "Finish active runs and stop the owning Desktop/daemon, then retry. The existing binary was preserved.";
      if (!allowLockedDestination) throw new Error(message, { cause: error });
      warn(`${message} Keeping it for this compilation-only build.`);
      return false;
    }
    return true;
  } finally {
    await remove(staged, { force: true });
  }
}

import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";

export function createDevCliPath(root, platform = process.platform) {
  const parent = join(root, ".multica", "desktop-cli");
  mkdirSync(parent, { recursive: true });
  return join(mkdtempSync(join(parent, "build-")), platform === "win32" ? "multica.exe" : "multica");
}

// Include embedded prompts/assets as well as Go code in the version identity.
export function devCliSourceDigest(serverDir) {
  const hash = createHash("sha256");
  function visit(dir) {
    for (const entry of readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = join(dir, entry.name);
      if (entry.isDirectory() && !["bin", ".git", ".multica", "node_modules"].includes(entry.name)) visit(file);
      else if (entry.isFile() && /\.(go|mod|sum|md|json|sql|yaml|yml|txt)$/.test(entry.name)) {
        hash.update(relative(serverDir, file).replaceAll("\\", "/") + "\0");
        hash.update(readFileSync(file));
      }
    }
  }
  visit(serverDir);
  return hash.digest("hex").slice(0, 24);
}

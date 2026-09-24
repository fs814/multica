// @vitest-environment node
import { resolve } from "path";
import { expect, it } from "vitest";
import { desktopDevCliPath } from "./dev-cli-path";

it("only accepts an absolute override for development", () => {
  const path = resolve("isolated", "multica.exe");
  expect(desktopDevCliPath(false, path)).toBe(path);
  expect(desktopDevCliPath(true, path)).toBeNull();
  expect(desktopDevCliPath(false, undefined)).toBeNull();
  expect(() => desktopDevCliPath(false, "relative.exe")).toThrow(/absolute/);
});

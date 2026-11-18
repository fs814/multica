// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseSkhdrc, prettyKeys } from "./keymaps";

const SAMPLE = `# skhd -- hotkeys for yabai
#
# long header prose that is not a binding

# focus window; at the edge of a display, move to the next display instead
alt + shift - h : yabai -m window --focus west  || yabai -m display --focus west
alt + shift - l : yabai -m window --focus east

# send window to display
alt + shift + ctrl - 0x21 : yabai -m window --display prev; \\
                            yabai -m display --focus prev
alt + shift - left -> : echo passthrough

:: resize @ : echo mode
.load "other"

# ---------------------------------------------------------------- app help
# cmd - h in cmux (physical Alt+H)
cmd - h [
    "cmux" : ~/bin/fslauncher keyhelp cmux
    *      ~
]
`;

describe("skhd keymaps", () => {
  it("prints modifiers as symbols and decodes keycodes and arrows", () => {
    expect(prettyKeys("alt + shift + ctrl - 0x21")).toBe("⌥ ⇧ ⌃ [");
    expect(prettyKeys("cmd - h")).toBe("⌘ H");
    expect(prettyKeys("alt + shift - left")).toBe("⌥ ⇧ ←");
    expect(prettyKeys("hyper - return")).toBe("⌘⌥⇧⌃ Return");
  });

  it("groups bindings under the comment above them, joining continuations and per-app blocks", () => {
    const keymap = parseSkhdrc(SAMPLE, "/home/me/.config/skhd/skhdrc");
    expect(keymap.source).toBe("/home/me/.config/skhd/skhdrc");
    expect(keymap.sections.map((s) => [s.title, s.bindings.length])).toEqual([
      ["focus window; at the edge of a display, move to the next display instead", 2],
      ["send window to display", 2],
      ["app help", 2],
    ]);
    expect(keymap.sections[0]!.bindings[0]).toEqual({
      keys: "⌥ ⇧ H", raw: "alt + shift - h", command: "yabai -m window --focus west || yabai -m display --focus west", app: null,
    });
    expect(keymap.sections[1]!.bindings[0]!.command).toBe("yabai -m window --display prev; yabai -m display --focus prev");
    expect(keymap.sections[2]!.bindings).toEqual([
      { keys: "⌘ H", raw: "cmd - h", command: "~/bin/fslauncher keyhelp cmux", app: "cmux" },
      { keys: "⌘ H", raw: "cmd - h", command: "(passed through)", app: "other apps" },
    ]);
  });
});

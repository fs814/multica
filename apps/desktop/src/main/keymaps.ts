import type { AppKeymap, KeymapBinding } from "@multica/core/apps";

/**
 * Parser for skhd's config (the hotkey daemon that drives yabai), turned into
 * sections of readable bindings for the Apps space's "Show keymappings".
 *
 * Handles: `mods - key : command`, `->` passthrough, `\` line continuations,
 * hex keycodes, and per-app blocks (`mods - key [ "App" : command ]`).
 * Ignores modes (`::name`), `.load`, `.blacklist`. A section title is the
 * first line of the comment block above a group of bindings.
 */

const MODIFIERS: Record<string, string> = {
  alt: "⌥", lalt: "⌥", ralt: "⌥", shift: "⇧", lshift: "⇧", rshift: "⇧",
  cmd: "⌘", lcmd: "⌘", rcmd: "⌘", ctrl: "⌃", lctrl: "⌃", rctrl: "⌃",
  fn: "fn", hyper: "⌘⌥⇧⌃", meh: "⌥⇧⌃",
};

/** macOS virtual key codes skhd configs commonly use for punctuation. */
const KEYCODES: Record<string, string> = {
  "0x21": "[", "0x1e": "]", "0x32": "`", "0x2a": "\\", "0x29": ";", "0x27": "'", "0x2b": ",", "0x2f": ".",
  "0x2c": "/", "0x18": "=", "0x1b": "-", "0x24": "Return", "0x30": "Tab", "0x31": "Space", "0x33": "Delete", "0x35": "Esc",
};

const NAMED_KEYS: Record<string, string> = {
  left: "←", right: "→", up: "↑", down: "↓", return: "Return", tab: "Tab", space: "Space",
  backspace: "Delete", escape: "Esc", delete: "Fwd Delete", home: "Home", end: "End", pageup: "PgUp", pagedown: "PgDn",
};

/** "alt + shift - 0x21" -> "⌥ ⇧ [" */
export function prettyKeys(raw: string): string {
  const dash = raw.lastIndexOf(" - ");
  const mods = dash >= 0 ? raw.slice(0, dash).split("+").map((m) => m.trim().toLowerCase()).filter(Boolean) : [];
  const key = (dash >= 0 ? raw.slice(dash + 3) : raw).trim();
  const lower = key.toLowerCase();
  const keyLabel = KEYCODES[lower] ?? NAMED_KEYS[lower] ?? (key.length === 1 ? key.toUpperCase() : key);
  return [...mods.map((m) => MODIFIERS[m] ?? m), keyLabel].join(" ");
}

function joinContinuations(text: string): string[] {
  const out: string[] = [];
  let pending = "";
  for (const line of text.split(/\r?\n/)) {
    if (/\\\s*$/.test(line)) {
      pending += line.replace(/\\\s*$/, " ");
      continue;
    }
    out.push(pending + line);
    pending = "";
  }
  if (pending) out.push(pending);
  return out;
}

const tidy = (command: string) => command.replace(/\s+/g, " ").trim();

export function parseSkhdrc(text: string, source: string): AppKeymap {
  const sections: AppKeymap["sections"] = [];
  let title = "";
  let titleTaken = true; // the comment block's first line is the title; later lines are prose
  let current: KeymapBinding[] | null = null;
  let block: { raw: string } | null = null;

  const add = (binding: KeymapBinding) => {
    if (!current) {
      current = [];
      sections.push({ title: title || "Bindings", bindings: current });
    }
    current.push(binding);
  };

  for (const line of joinContinuations(text)) {
    const trimmed = line.trim();
    if (block) {
      if (trimmed === "]") { block = null; continue; }
      const m = /^("([^"]+)"|\*)\s*(~|:\s*(.+))$/.exec(trimmed);
      if (m) {
        const app = m[2] ?? "other apps";
        add({ keys: prettyKeys(block.raw), raw: block.raw, command: m[4] ? tidy(m[4]) : "(passed through)", app });
      }
      continue;
    }
    if (!trimmed) { titleTaken = true; continue; }
    if (trimmed.startsWith("#")) {
      if (titleTaken) {
        // A new comment block starts a new section (once bindings follow it).
        const text = trimmed.replace(/^#+\s*/, "").replace(/^-+\s*/, "").trim();
        if (text) {
          title = text;
          titleTaken = false;
          current = null;
        }
      }
      continue;
    }
    titleTaken = true;
    if (trimmed.startsWith("::") || trimmed.startsWith(".")) continue;
    const blockStart = /^([^:"[]+?)\s*\[\s*$/.exec(trimmed);
    if (blockStart) { block = { raw: blockStart[1]!.trim() }; continue; }
    const m = /^([^:"[]+?)\s*(->)?\s*:\s*(.+)$/.exec(trimmed);
    if (m) add({ keys: prettyKeys(m[1]!.trim()), raw: m[1]!.trim(), command: tidy(m[3]!), app: null });
  }
  return { source, sections };
}

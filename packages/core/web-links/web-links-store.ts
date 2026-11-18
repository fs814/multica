"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../platform/workspace-storage";
import { seedDefaultLinks } from "./default-links";
import { normalizeCategory, normalizeWebUrl, parseWebLinks, type WebLink } from "./models";

interface WebLinksState {
  links: WebLink[];
  /** Default link ids already offered to this workspace (see default-links.ts). */
  seeded: string[];
  /** Categories folded shut in the Web tab ("" = the uncategorized group). */
  collapsed: string[];
  toggleCategory: (category: string) => void;
  save: (link: WebLink) => void;
  remove: (id: string) => void;
}

export const useWebLinksStore = create<WebLinksState>()(persist((set) => ({
  links: [],
  seeded: [],
  collapsed: [],
  toggleCategory: (category) => set((state) => ({
    collapsed: state.collapsed.includes(category)
      ? state.collapsed.filter((c) => c !== category)
      : [...state.collapsed, category],
  })),
  save: (link) => {
    const url = normalizeWebUrl(link.url);
    if (!url || !link.name.trim() || !link.id) throw new Error("Invalid web link");
    const category = normalizeCategory(link.category);
    const saved: WebLink = { id: link.id, name: link.name.trim(), url, ...(category ? { category } : {}) };
    set((state) => ({ links: state.links.some((item) => item.id === link.id)
      ? state.links.map((item) => item.id === link.id ? saved : item)
      : [...state.links, saved] }));
  },
  remove: (id) => set((state) => ({ links: state.links.filter((item) => item.id !== id) })),
}), {
  name: "multica_web_links",
  storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
  partialize: (state) => ({ links: state.links, seeded: state.seeded, collapsed: state.collapsed }),
  merge: (persisted, current) => {
    const stored = persisted && typeof persisted === "object" ? persisted as Record<string, unknown> : {};
    const seeded = Array.isArray(stored.seeded) ? stored.seeded.filter((id): id is string => typeof id === "string") : [];
    const collapsed = Array.isArray(stored.collapsed) ? stored.collapsed.filter((c): c is string => typeof c === "string") : [];
    return { ...current, collapsed, ...seedDefaultLinks(parseWebLinks(stored.links), seeded) };
  },
}));

registerForWorkspaceRehydration(() => useWebLinksStore.persist.rehydrate());

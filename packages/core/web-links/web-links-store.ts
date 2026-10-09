"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import { createWorkspaceAwareStorage, registerForWorkspaceRehydration } from "../platform/workspace-storage";
import { normalizeWebUrl, parseWebLinks, type WebLink } from "./models";

interface WebLinksState {
  links: WebLink[];
  save: (link: WebLink) => void;
  remove: (id: string) => void;
}

export const useWebLinksStore = create<WebLinksState>()(persist((set) => ({
  links: [],
  save: (link) => {
    const url = normalizeWebUrl(link.url);
    if (!url || !link.name.trim() || !link.id) throw new Error("Invalid web link");
    const saved = { ...link, name: link.name.trim(), url };
    set((state) => ({ links: state.links.some((item) => item.id === link.id)
      ? state.links.map((item) => item.id === link.id ? saved : item)
      : [...state.links, saved] }));
  },
  remove: (id) => set((state) => ({ links: state.links.filter((item) => item.id !== id) })),
}), {
  name: "multica_web_links",
  storage: createJSONStorage(() => createWorkspaceAwareStorage(defaultStorage)),
  partialize: (state) => ({ links: state.links }),
  merge: (persisted, current) => ({
    ...current,
    links: parseWebLinks(persisted && typeof persisted === "object" && "links" in persisted ? persisted.links : []),
  }),
}));

registerForWorkspaceRehydration(() => useWebLinksStore.persist.rehydrate());

"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";

/**
 * Apps-page view preferences for this device: which category groups are
 * folded, and which apps were deleted from the list (by path; the apps
 * themselves stay installed).
 */
interface AppsViewState {
  collapsed: string[];
  hidden: string[];
  toggleCategory: (category: string) => void;
  hide: (path: string) => void;
  restoreAll: () => void;
}

export const useAppsViewStore = create<AppsViewState>()(persist((set) => ({
  collapsed: [],
  hidden: [],
  hide: (path) => set((state) => (state.hidden.includes(path) ? state : { hidden: [...state.hidden, path] })),
  restoreAll: () => set({ hidden: [] }),
  toggleCategory: (category) => set((state) => ({
    collapsed: state.collapsed.includes(category)
      ? state.collapsed.filter((c) => c !== category)
      : [...state.collapsed, category],
  })),
}), {
  name: "multica_apps_view",
  storage: createJSONStorage(() => defaultStorage),
  partialize: (state) => ({ collapsed: state.collapsed, hidden: state.hidden }),
  merge: (persisted, current) => {
    const stored = persisted && typeof persisted === "object" ? persisted as Record<string, unknown> : {};
    const strings = (value: unknown) => (Array.isArray(value) ? value.filter((c): c is string => typeof c === "string") : []);
    return { ...current, collapsed: strings(stored.collapsed), hidden: strings(stored.hidden) };
  },
}));

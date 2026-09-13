import { expect, it } from "vitest";
import { useTabStore } from "./tab-store";

it("review: close other tabs honors a dirty workflow cancellation", () => {
  const store = useTabStore.getState();
  store.reset();
  store.switchWorkspace("acme");
  const keep = useTabStore.getState().byWorkspace.acme.tabs[0].id;
  const dirty = store.addTab("/acme/workflows/review-draft", "Dirty workflow");
  store.setActiveTab(dirty);
  let prompts = 0;
  const cancel = (event: Event) => {
    prompts++;
    event.preventDefault();
  };
  window.addEventListener("multica:before-navigate", cancel);
  try {
    store.closeOtherTabs(keep);
    console.log(
      JSON.stringify({
        prompts,
        dirtyTabSurvived: useTabStore
          .getState()
          .byWorkspace.acme.tabs.some((t) => t.id === dirty),
      }),
    );
    expect(
      useTabStore.getState().byWorkspace.acme.tabs.some((t) => t.id === dirty),
    ).toBe(true);
  } finally {
    window.removeEventListener("multica:before-navigate", cancel);
    store.reset();
  }
});

it("review: desktop toolbar history honors dirty workflow cancellation", () => {
  const store = useTabStore.getState();
  store.reset();
  store.switchWorkspace("acme");
  const dirtyPath = "/acme/workflows/review-draft";
  store.navigateActiveSession(dirtyPath);
  let prompts = 0;
  const cancel = (event: Event) => {
    prompts++;
    event.preventDefault();
  };
  window.addEventListener("multica:before-navigate", cancel);
  try {
    store.goBack();
    const group = useTabStore.getState().byWorkspace.acme;
    const url = group.tabs.find((t) => t.id === group.activeTabId)?.url;
    console.log(JSON.stringify({ prompts, url }));
    expect(url).toBe(dirtyPath);
  } finally {
    window.removeEventListener("multica:before-navigate", cancel);
    store.reset();
  }
});

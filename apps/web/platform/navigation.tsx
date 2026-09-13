"use client";

import { Suspense, useEffect, useSyncExternalStore } from "react";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "@multica/views/navigation";
import { canGoBackInApp } from "./in-app-history";

/**
 * Web half of the `multica:navigate` bridge — the event shared content
 * (comments, chat, issue descriptions) fires when a link resolves to an in-app
 * destination. A plain click ("push") is a router push in place. A modifier
 * click normally never reaches here on web — real anchors leave it to the
 * browser — but the editor must intercept every click (contenteditable
 * anchors don't navigate natively), and for those `window.open` is the
 * closest the web can get: JS cannot open a background tab, so both tab
 * dispositions land as a foreground browser tab.
 */
function useInternalLinkHandler(router: ReturnType<typeof useRouter>) {
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (e as CustomEvent<{ path?: string; disposition?: string }>)
        .detail;
      const path = detail?.path;
      if (!path) return;
      if (
        detail?.disposition === "background-tab" ||
        detail?.disposition === "foreground-tab"
      ) {
        window.open(
          window.location.origin + path,
          "_blank",
          "noopener,noreferrer",
        );
        return;
      }
      if (
        window.dispatchEvent(
          new CustomEvent("multica:before-navigate", {
            cancelable: true,
            detail: { pathname: window.location.pathname, destination: path },
          }),
        )
      )
        router.push(path);
    };
    window.addEventListener("multica:navigate", handler);
    return () => window.removeEventListener("multica:navigate", handler);
  }, [router]);
}

/**
 * The fragment is client-only state Next.js never surfaces: `usePathname()`
 * drops it, and a `router.replace("/x#y")` mutates `window.location` without
 * a render of its own. Reading it through an external store re-reads the URL
 * on every render and re-renders on the events that change it behind React's
 * back, so `adapter.hash` is never a stale copy.
 */
function subscribeToHash(onStoreChange: () => void): () => void {
  window.addEventListener("hashchange", onStoreChange);
  window.addEventListener("popstate", onStoreChange);
  return () => {
    window.removeEventListener("hashchange", onStoreChange);
    window.removeEventListener("popstate", onStoreChange);
  };
}

function NavigationProviderInner({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const hash = useSyncExternalStore(
    subscribeToHash,
    () => window.location.hash,
    () => "",
  );
  useInternalLinkHandler(router);
  useEffect(() => {
    // Chromium's Navigation API can cancel browser back/forward before Next
    // unmounts the editor. Adapter-driven traversal already has its own guard.
    const browser = (window as unknown as { navigation?: EventTarget })
      .navigation;
    const traverse = (event: Event) => {
      const detail = event as Event & {
        navigationType?: string;
        destination?: { url: string };
      };
      if (detail.navigationType !== "traverse" || !event.cancelable) return;
      const destination = detail.destination
        ? new URL(detail.destination.url)
        : null;
      if (
        !window.dispatchEvent(
          new CustomEvent("multica:before-navigate", {
            cancelable: true,
            detail: {
              pathname,
              destination: destination
                ? destination.pathname + destination.search
                : undefined,
            },
          }),
        )
      )
        event.preventDefault();
    };
    browser?.addEventListener?.("navigate", traverse);
    return () => browser?.removeEventListener?.("navigate", traverse);
  }, [pathname]);

  const adapter: NavigationAdapter = {
    push: router.push,
    replace: router.replace,
    back: router.back,
    forward: router.forward,
    canGoBack: canGoBackInApp,
    pathname,
    searchParams: new URLSearchParams(searchParams.toString()),
    hash,
    getShareableUrl: (path: string) =>
      typeof window === "undefined" ? path : window.location.origin + path,
    // router.prefetch is a no-op in dev mode by Next.js design; in production
    // it warms the RSC payload + route chunk so the next push() commits with
    // no network round-trip. Safe to call repeatedly — Next dedupes internally.
    prefetch: (path: string) => {
      router.prefetch(path);
    },
  };

  return <NavigationProvider value={adapter}>{children}</NavigationProvider>;
}

export function WebNavigationProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <Suspense>
      <NavigationProviderInner>{children}</NavigationProviderInner>
    </Suspense>
  );
}

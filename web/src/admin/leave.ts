// sessionStorage is per tab AND per origin: every page of ryftlab.de loaded in this tab (or a
// duplicate of it) can read what /admin stored. So the session is cleared when the tab leaves
// /admin: on `pagehide`, unless the Navigation API told us the next document is /admin itself
// (a script-initiated reload or navigation). A reload by the browser (reload button, pull to
// refresh) and the address bar fire no `navigate` event, so they look like leaving: the
// operator signs in again, the safe direction (web D16). This is the only use of the Navigation
// API and of `pagehide` in the bundle (tests/admin-source.test.ts and tests/output.test.ts pin that).
//
// Without the Navigation API every pagehide clears, a reload included: the operator signs in
// again, which is the safe direction. A page restored from the back/forward cache is not exempt
// either: its storage is empty and the next api call asks to sign in.

export interface NavigateEventLike {
  destination: { url: string; sameDocument: boolean };
}

export interface WindowLike {
  navigation?: {
    currentEntry?: { url: string | null } | null;
    addEventListener(type: 'navigate', fn: (e: NavigateEventLike) => void): void;
  };
  addEventListener(type: 'pagehide', fn: () => void): void;
}

/** True when `destination` is the same page as `current` (same origin and path; the fragment does not count). */
export function isSamePage(destination: string, current: string | null | undefined): boolean {
  if (!current) return false;
  try {
    const d = new URL(destination);
    const c = new URL(current);
    return d.origin === c.origin && d.pathname === c.pathname;
  } catch {
    return false;
  }
}

/** Calls `clear` on pagehide, except when the next document is this page again. */
export function clearOnLeave(win: WindowLike, clear: () => void): void {
  let staying = false;
  win.navigation?.addEventListener('navigate', (e) => {
    // A fragment change (#users) stays in this document and never unloads it.
    if (e.destination.sameDocument) return;
    staying = isSamePage(e.destination.url, win.navigation?.currentEntry?.url);
  });
  win.addEventListener('pagehide', () => {
    if (!staying) clear();
  });
}

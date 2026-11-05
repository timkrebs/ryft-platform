// The operator's session lives in sessionStorage: per tab, gone when the tab closes, and
// unreadable from any other page of the origin. Nothing else is stored, and nothing goes
// to localStorage or a cookie (web D16).
import type { Session } from './types';

export const SESSION_KEY = 'ryft.admin.session';

export interface KV {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export function loadSession(kv: KV): Session | null {
  try {
    const raw = kv.getItem(SESSION_KEY);
    if (!raw) return null;
    const s: unknown = JSON.parse(raw);
    if (typeof s !== 'object' || s === null) return null;
    const o = s as Record<string, unknown>;
    if (typeof o.access_token !== 'string' || typeof o.refresh_token !== 'string') return null;
    if (typeof o.expires_at !== 'number' || !Number.isFinite(o.expires_at)) return null;
    return { access_token: o.access_token, refresh_token: o.refresh_token, expires_at: o.expires_at };
  } catch {
    return null;
  }
}

export function saveSession(kv: KV, s: Session): void {
  try {
    kv.setItem(
      SESSION_KEY,
      JSON.stringify({
        access_token: s.access_token,
        refresh_token: s.refresh_token,
        expires_at: s.expires_at,
      }),
    );
  } catch {
    // Storage is full or blocked: the session then lasts only until the page reloads.
  }
}

export function clearSession(kv: KV): void {
  try {
    kv.removeItem(SESSION_KEY);
  } catch {
    // Nothing to clear.
  }
}

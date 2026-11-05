// The session in sessionStorage, refreshed shortly before the access token expires.
import { AuthError, type AuthClient } from './gotrue';
import { clearSession, loadSession, saveSession, type KV } from './storage';
import type { Session } from './types';

/** Refresh when less than this is left of the access token. */
export const REFRESH_MARGIN_MS = 60_000;

export type Sessions = ReturnType<typeof createSessions>;

export function createSessions(auth: Pick<AuthClient, 'refresh'>, kv: KV, now: () => number = Date.now) {
  // One refresh at a time: GoTrue's refresh tokens are single-use.
  let inflight: Promise<Session | null> | null = null;

  const current = () => loadSession(kv);

  /** Refreshes unconditionally. null: there is no session, or the refresh token was refused (it is then cleared). */
  function refresh(): Promise<Session | null> {
    if (inflight) return inflight;
    const s = current();
    if (!s) return Promise.resolve(null);
    inflight = (async () => {
      try {
        const next = await auth.refresh(s.refresh_token);
        saveSession(kv, next);
        return next;
      } catch (e) {
        // No answer at all is not a reason to end the session; a refusal is.
        if (e instanceof AuthError && e.status === 0) throw e;
        clearSession(kv);
        return null;
      } finally {
        inflight = null;
      }
    })();
    return inflight;
  }

  return {
    current,
    set: (s: Session) => saveSession(kv, s),
    clear: () => clearSession(kv),
    refresh,
    /** The session, refreshed when it is about to expire. */
    async fresh(): Promise<Session | null> {
      const s = current();
      if (!s) return null;
      if (s.expires_at * 1000 - now() > REFRESH_MARGIN_MS) return s;
      return refresh();
    },
  };
}

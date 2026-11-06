// The session in sessionStorage, refreshed shortly before the access token expires.
import { AuthError, type AuthClient } from './gotrue';
import { clearSession, loadSession, saveSession, type KV } from './storage';
import type { Session } from './types';

/** Refresh when less than this is left of the access token. */
export const REFRESH_MARGIN_MS = 60_000;

/**
 * The answers that mean the refresh token is no good: refused (400, e.g. refresh_token_not_found or
 * _already_used), or the account is gone or banned (401, 403). Anything else (no answer, 429, a 5xx,
 * an answer the page does not understand) says nothing about the token, so the session is kept.
 */
const REFUSED = new Set([400, 401, 403]);

export type Sessions = ReturnType<typeof createSessions>;

export function createSessions(auth: Pick<AuthClient, 'refresh'>, kv: KV, now: () => number = Date.now) {
  // One refresh at a time: GoTrue's refresh tokens are single-use.
  let inflight: Promise<Session | null> | null = null;

  const current = () => loadSession(kv);

  /**
   * Refreshes unconditionally. null: there is no session, or the refresh token was refused (it is then
   * cleared). Throws the AuthError, keeping the session, when the sign-in service did not decide.
   */
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
        // A refusal ends the session; no answer, a rate limit or a server error does not.
        if (e instanceof AuthError && REFUSED.has(e.status)) {
          clearSession(kv);
          return null;
        }
        throw e;
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

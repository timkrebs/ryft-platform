// Supabase Auth (GoTrue) over its REST API, for exactly what the admin page needs:
// password sign-in, TOTP enrol / challenge / verify, refresh and sign out. No supabase-js
// (web D15): nothing from a third party runs on this page. Every call omits cookies, is
// never cached and sends no referrer. Tokens are never logged.
import type { Session } from './types';

export interface AuthConfig {
  url: string;
  key: string;
}

export interface AuthDeps {
  fetch?: typeof fetch;
  /** Milliseconds since the epoch. */
  now?: () => number;
}

export class AuthError extends Error {
  constructor(
    message: string,
    /** HTTP status; 0 when there was no answer at all. */
    readonly status: number,
    /** GoTrue's `error_code`, when it sent one. */
    readonly code: string | null,
  ) {
    super(message);
    this.name = 'AuthError';
  }
}

export interface AuthUser {
  id: string;
  factors: { id: string; factor_type: string; status: string }[];
}

export interface TotpEnrolment {
  id: string;
  /** A data: URL for <img src>, or null if GoTrue sent nothing usable. */
  qr: string | null;
  secret: string;
  uri: string;
}

export type AuthClient = ReturnType<typeof createAuthClient>;

const KNOWN: Record<string, string> = {
  invalid_credentials: 'Email or password is wrong.',
  mfa_verification_failed: 'That code is wrong or has expired. Try the next one.',
  mfa_challenge_expired: 'That code has expired. Try the next one.',
  over_request_rate_limit: 'Too many attempts. Wait a minute and try again.',
  too_many_requests: 'Too many attempts. Wait a minute and try again.',
  over_email_send_rate_limit: 'Too many attempts. Wait a minute and try again.',
};

export function createAuthClient(cfg: AuthConfig, deps: AuthDeps = {}) {
  const doFetch: typeof fetch = deps.fetch ?? ((input, init) => globalThis.fetch(input, init));
  const now = deps.now ?? Date.now;

  async function call(
    method: string,
    path: string,
    opts: { token?: string; body?: unknown } = {},
  ): Promise<unknown> {
    const headers: Record<string, string> = { apikey: cfg.key, Accept: 'application/json' };
    if (opts.token) headers.Authorization = `Bearer ${opts.token}`;
    if (opts.body !== undefined) headers['Content-Type'] = 'application/json';
    let res: Response;
    try {
      res = await doFetch(`${cfg.url}/auth/v1${path}`, {
        method,
        headers,
        body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
        credentials: 'omit',
        cache: 'no-store',
        referrerPolicy: 'no-referrer',
      });
    } catch {
      throw new AuthError(
        'No answer from the sign-in service. Check your connection and try again.',
        0,
        'network',
      );
    }
    const text = await res.text().catch(() => '');
    let data: unknown = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = null;
      }
    }
    if (!res.ok) throw toError(res.status, data);
    return data;
  }

  function toSession(data: unknown): Session {
    const o = (data ?? {}) as Record<string, unknown>;
    if (typeof o.access_token !== 'string' || typeof o.refresh_token !== 'string') {
      throw new AuthError(
        'The sign-in service sent an answer this page does not understand.',
        502,
        'bad_response',
      );
    }
    const expiresAt =
      typeof o.expires_at === 'number' && Number.isFinite(o.expires_at)
        ? o.expires_at
        : Math.floor(now() / 1000) + (typeof o.expires_in === 'number' ? o.expires_in : 3600);
    return { access_token: o.access_token, refresh_token: o.refresh_token, expires_at: expiresAt };
  }

  return {
    /** Password grant. The session is aal1 until a TOTP is verified. */
    async signInWithPassword(email: string, password: string): Promise<Session> {
      return toSession(await call('POST', '/token?grant_type=password', { body: { email, password } }));
    },

    async refresh(refreshToken: string): Promise<Session> {
      return toSession(
        await call('POST', '/token?grant_type=refresh_token', { body: { refresh_token: refreshToken } }),
      );
    },

    /** The user, with the factors the page needs to choose between enrol and challenge. */
    async getUser(token: string): Promise<AuthUser> {
      const o = ((await call('GET', '/user', { token })) ?? {}) as Record<string, unknown>;
      const factors = Array.isArray(o.factors) ? o.factors : [];
      return {
        id: typeof o.id === 'string' ? o.id : '',
        factors: factors
          .filter((f): f is Record<string, unknown> => typeof f === 'object' && f !== null)
          .map((f) => ({
            id: String(f.id ?? ''),
            factor_type: String(f.factor_type ?? ''),
            status: String(f.status ?? ''),
          }))
          .filter((f) => f.id !== ''),
      };
    },

    async enrolTotp(token: string, friendlyName: string): Promise<TotpEnrolment> {
      const o = ((await call('POST', '/factors', {
        token,
        body: { factor_type: 'totp', friendly_name: friendlyName, issuer: 'RYFT admin' },
      })) ?? {}) as Record<string, unknown>;
      const t = (typeof o.totp === 'object' && o.totp !== null ? o.totp : {}) as Record<string, unknown>;
      if (typeof o.id !== 'string' || typeof t.secret !== 'string') {
        throw new AuthError(
          'The sign-in service sent an answer this page does not understand.',
          502,
          'bad_response',
        );
      }
      return {
        id: o.id,
        qr: qrDataUrl(t.qr_code),
        secret: t.secret,
        uri: typeof t.uri === 'string' ? t.uri : '',
      };
    },

    async unenroll(token: string, factorId: string): Promise<void> {
      await call('DELETE', `/factors/${encodeURIComponent(factorId)}`, { token });
    },

    /** Starts a challenge for a factor; the id goes into verify. */
    async challenge(token: string, factorId: string): Promise<string> {
      const o = ((await call('POST', `/factors/${encodeURIComponent(factorId)}/challenge`, {
        token,
        body: {},
      })) ?? {}) as Record<string, unknown>;
      if (typeof o.id !== 'string') {
        throw new AuthError(
          'The sign-in service sent an answer this page does not understand.',
          502,
          'bad_response',
        );
      }
      return o.id;
    },

    /** Verifies the code; the new session is aal2 with a fresh `totp` entry in `amr`. */
    async verify(token: string, factorId: string, challengeId: string, code: string): Promise<Session> {
      return toSession(
        await call('POST', `/factors/${encodeURIComponent(factorId)}/verify`, {
          token,
          body: { challenge_id: challengeId, code },
        }),
      );
    },

    /**
     * Ends sessions: `global` (the default, the operator's "Sign out") every session of the
     * account, `local` only this one (an account that is not an operator's, a second 401: its
     * app sessions are left alone).
     */
    async signOut(token: string, scope: 'global' | 'local' = 'global'): Promise<void> {
      await call('POST', `/logout?scope=${scope}`, { token });
    },
  };
}

function toError(status: number, data: unknown): AuthError {
  const o = (typeof data === 'object' && data !== null ? data : {}) as Record<string, unknown>;
  const code = typeof o.error_code === 'string' ? o.error_code : typeof o.code === 'string' ? o.code : null;
  if (code && KNOWN[code]) return new AuthError(KNOWN[code], status, code);
  const text = [o.msg, o.error_description, o.message, o.error].find(
    (v) => typeof v === 'string' && v !== '',
  );
  return new AuthError(
    typeof text === 'string' ? text : `The sign-in service answered ${status}.`,
    status,
    code,
  );
}

/**
 * GoTrue returns the QR code as SVG markup (documented as "prepend data:image/svg+xml;utf-8,").
 * Its SVG writer (SVGo) starts the markup with an XML declaration and a comment:
 * `<?xml version="1.0"?>\n<!-- Generated by SVGo -->\n<svg …`, so an optional declaration and
 * comments may come before `<svg`. Accept that markup, the same behind the documented prefix, or
 * a ready non-markup data URL, and nothing else. The result only ever becomes an <img> src, and
 * an SVG in an <img> runs no script and loads nothing.
 */
// `<!-{2}` and `-{2}>`, not the literal comment delimiters: an inline script must not contain an
// HTML comment opener (scripts/lib/admin-bundle.mjs refuses one).
const SVG_MARKUP = /^(?:<\?xml[^<>]*\?>\s*)?(?:<!-{2}(?:(?!-{2}>)[\s\S])*-{2}>\s*)*<svg[\s>/]/;

export function qrDataUrl(qr: unknown): string | null {
  if (typeof qr !== 'string') return null;
  const s = qr.trim();
  const markup = s.startsWith('data:image/svg+xml;utf-8,') ? s.slice('data:image/svg+xml;utf-8,'.length) : s;
  if (SVG_MARKUP.test(markup)) return `data:image/svg+xml;utf-8,${encodeURIComponent(markup)}`;
  if (/^data:image\/svg\+xml(;[a-z0-9=-]+)*,[^<\s]/i.test(s)) return s;
  return null;
}

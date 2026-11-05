import { readFileSync } from 'node:fs';
import type { Page, Route } from '@playwright/test';

// A stand-in for the two services the admin page talks to: Supabase Auth (GoTrue) and the
// api's /v1/admin/* routes. It is installed with page.route(), so the browser still enforces
// the page's CSP (connect-src) and CORS before a request reaches it, and every other
// non-local request is aborted. State lives in the Stub: accounts, factors, sessions,
// invites, users, feedback. The answers follow docs/API.md → Admin routes and GoTrue's REST
// shapes (the same ones tests/admin-gotrue.test.ts pins).
const config = JSON.parse(readFileSync('config/admin.json', 'utf8')) as {
  apiUrl: string;
  supabaseUrl: string;
  supabaseKey: string;
};
export const API = config.apiUrl;
export const AUTH = config.supabaseUrl;
export const PASSWORD = 'correct horse battery staple';
export const EMAIL = 'admin@example.test';
export const TOTP = '123456';

const b64 = (o: unknown) => Buffer.from(JSON.stringify(o)).toString('base64url');
const DAY = 86_400_000;
const daysFromNow = (d: number) => new Date(Date.now() + d * DAY).toISOString();

interface TokenInfo {
  aal: 'aal1' | 'aal2';
  role: string | null;
  expired: boolean;
}

export interface Invite {
  code: string;
  note: string;
  max_uses: number;
  uses: number;
  expires_at: string | null;
  created_at: string;
  email: string | null;
  created_by: { kind: 'key' | 'user'; id: string | null };
  revoked_at: string | null;
  status: 'open' | 'used' | 'expired' | 'revoked';
  link: string;
  uses_by: { user_id: string; short_id: string; email: string | null; private_relay: boolean; at: string }[];
}

export interface Seen {
  method: string;
  path: string;
  search: string;
  body: unknown;
  authorization: string | null;
}

export class Stub {
  /** The account's role claim; null: an ordinary app account. */
  role: string | null = 'admin';
  /** Verified TOTP factor ids; empty: the page must enrol first. */
  factors: { id: string; status: 'verified' | 'unverified' }[] = [];
  /** While true every admin call answers 403 mfa_required until a TOTP is verified. */
  mfaStale = false;
  /** Keep answering mfa_required even after a verify (the fallback: the page signs out). */
  mfaSticky = false;
  /** Abort every api request (no CORS headers, an edge 429 or a 502 look like this to the page). */
  apiDown = false;
  /** A status for every refresh_token grant (429, 503: GoTrue did not decide); null answers it. */
  refreshStatus: number | null = null;
  /** Answer the next N api requests matching `METHOD /path` with this status (a server refusal). */
  failNext = new Map<string, { status: number; times: number }>();
  /** Hold the answer to `METHOD /path?search` (api or Auth) this many ms; the request is recorded at once. */
  delay = new Map<string, number>();
  seen: Seen[] = [];

  private tokens = new Map<string, TokenInfo>();
  private refreshTokens = new Map<string, 'aal1' | 'aal2'>();
  private n = 0;
  private codes = 0;

  invites: Invite[] = [];
  users = [
    {
      id: '9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a',
      short_id: '9f8e7d6c',
      created_at: '2026-11-05T18:00:02Z',
      provider: 'email',
      email: 'anna@example.com',
      beta_access: false,
      last_sync_at: '2026-11-05T18:20:00Z',
    },
    {
      id: '0a1b2c3d-4e5f-4061-8283-949a5b6c7d8e',
      short_id: '0a1b2c3d',
      created_at: '2026-10-22T07:41:00Z',
      provider: 'apple',
      email: 'x7k2m9q4@privaterelay.appleid.com',
      beta_access: true,
      last_sync_at: null,
    },
  ];
  feedback = [
    {
      id: 42,
      user_short_id: '9f8e7d6c',
      category: 'bug',
      message: 'Sleep stages are empty for last night.\nSecond line.',
      created_at: daysFromNow(-0.2),
      resolved_at: null as string | null,
    },
    {
      id: 43,
      user_short_id: '0a1b2c3d',
      category: 'idea',
      message: '<img src=x onerror="window.__xss=1"><script>window.__xss=2</script>',
      created_at: daysFromNow(-1.2),
      resolved_at: null as string | null,
    },
    {
      // Outside the page's 30-day window: the api leaves it out when asked with `since`.
      id: 7,
      user_short_id: '0a1b2c3d',
      category: 'bug',
      message: 'An old report from the first wave.',
      created_at: daysFromNow(-40),
      resolved_at: null as string | null,
    },
  ];

  constructor() {
    this.invites.push({
      code: 'RYFT-H2NP-4WCX',
      note: '<b onmouseover="window.__xss=3">Ben</b>',
      max_uses: 1,
      uses: 1,
      expires_at: '2026-11-19T10:00:00Z',
      created_at: '2026-11-05T10:00:00Z',
      email: 'b***@e***.com',
      created_by: { kind: 'key', id: null },
      revoked_at: null,
      status: 'used',
      link: 'https://ryftapp.de/invite/RYFT-H2NP-4WCX',
      uses_by: [
        {
          user_id: '9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a',
          short_id: '9f8e7d6c',
          email: 'a***@e***.com',
          private_relay: false,
          at: '2026-11-05T18:02:11Z',
        },
      ],
    });
  }

  /** Makes every access token issued so far fail with 401 (the page then refreshes). */
  expireAccessTokens() {
    for (const t of this.tokens.values()) t.expired = true;
  }

  count(method: string, path: string | RegExp) {
    return this.seen.filter(
      (s) => s.method === method && (typeof path === 'string' ? s.path === path : path.test(s.path)),
    ).length;
  }

  private mint(aal: 'aal1' | 'aal2') {
    this.n += 1;
    const now = Math.floor(Date.now() / 1000);
    const amr =
      aal === 'aal2'
        ? [
            { method: 'password', timestamp: now },
            { method: 'totp', timestamp: now },
          ]
        : [{ method: 'password', timestamp: now }];
    const payload = {
      iss: `${AUTH}/auth/v1`,
      sub: '1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081',
      aud: 'authenticated',
      role: 'authenticated',
      aal,
      amr,
      app_metadata: this.role ? { provider: 'email', role: this.role } : { provider: 'email' },
      iat: now,
      exp: now + 3600,
    };
    const access = `${b64({ alg: 'ES256', typ: 'JWT' })}.${b64(payload)}.sig${this.n}`;
    const refresh = `refresh-${this.n}`;
    this.tokens.set(access, { aal, role: this.role, expired: false });
    this.refreshTokens.set(refresh, aal);
    return {
      access_token: access,
      token_type: 'bearer',
      expires_in: 3600,
      expires_at: now + 3600,
      refresh_token: refresh,
      user: { id: payload.sub, email: EMAIL },
    };
  }

  /** Installs the stub on a page: local requests pass, the two origins are answered, the rest is aborted. */
  async install(page: Page) {
    const local = (url: URL) => url.hostname === 'localhost' || url.hostname === '127.0.0.1';
    await page.route(
      (url) => !local(url) && url.origin !== API && url.origin !== AUTH,
      (route) => route.abort(),
    );
    await page.route(`${AUTH}/**`, (route) => this.auth(route));
    await page.route(`${API}/**`, (route) => this.api(route));
  }

  private cors(route: Route) {
    const origin = route.request().headers()['origin'] ?? '';
    return {
      'access-control-allow-origin': origin,
      'access-control-expose-headers': 'X-Request-ID, Retry-After',
      vary: 'Origin',
    };
  }

  private reply(route: Route, status: number, body?: unknown, headers: Record<string, string> = {}) {
    return route.fulfill({
      status,
      headers: {
        ...this.cors(route),
        ...(body === undefined ? {} : { 'content-type': 'application/json' }),
        'x-request-id': `req-${++this.n}`,
        ...headers,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  }

  private preflight(route: Route) {
    const h = route.request().headers();
    return route.fulfill({
      status: 204,
      headers: {
        ...this.cors(route),
        'access-control-allow-methods': 'GET, POST, DELETE',
        'access-control-allow-headers':
          h['access-control-request-headers'] ?? 'authorization, content-type, apikey',
        'access-control-max-age': '600',
      },
    });
  }

  private record(route: Route) {
    const r = route.request();
    const url = new URL(r.url());
    let body: unknown = null;
    try {
      body = r.postDataJSON();
    } catch {
      body = null;
    }
    const s: Seen = {
      method: r.method(),
      path: url.pathname,
      search: url.search,
      body,
      authorization: r.headers()['authorization'] ?? null,
    };
    this.seen.push(s);
    return s;
  }

  // ---- Supabase Auth ---------------------------------------------------------------------
  private async auth(route: Route) {
    if (route.request().method() === 'OPTIONS') return this.preflight(route);
    const s = this.record(route);
    const wait = this.delay.get(`${s.method} ${s.path}${s.search}`);
    if (wait) await new Promise((r) => setTimeout(r, wait));
    // Supabase's gateway answers this before GoTrue sees the request.
    if (route.request().headers()['apikey'] !== config.supabaseKey) {
      return this.reply(route, 401, { message: 'No API key found in request' });
    }
    const bearer = s.authorization?.replace(/^Bearer /i, '') ?? null;
    const info = bearer ? this.tokens.get(bearer) : undefined;
    const body = (s.body ?? {}) as Record<string, string>;
    const fail = (status: number, error_code: string, msg: string) =>
      this.reply(route, status, { code: status, error_code, msg });

    if (s.path === '/auth/v1/token' && s.search === '?grant_type=password') {
      if (body.email !== EMAIL || body.password !== PASSWORD)
        return fail(400, 'invalid_credentials', 'Invalid login credentials');
      return this.reply(route, 200, this.mint('aal1'));
    }
    if (s.path === '/auth/v1/token' && s.search === '?grant_type=refresh_token') {
      if (this.refreshStatus !== null) {
        return fail(
          this.refreshStatus,
          this.refreshStatus === 429 ? 'over_request_rate_limit' : 'unexpected_failure',
          'try later',
        );
      }
      const aal = this.refreshTokens.get(body.refresh_token ?? '');
      if (!aal) return fail(400, 'refresh_token_not_found', 'Invalid Refresh Token: Refresh Token Not Found');
      this.refreshTokens.delete(body.refresh_token!); // single use
      return this.reply(route, 200, this.mint(aal));
    }
    if (!info) return fail(401, 'bad_jwt', 'invalid JWT');
    if (s.path === '/auth/v1/user' && s.method === 'GET') {
      return this.reply(route, 200, {
        id: '1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081',
        email: EMAIL,
        factors: this.factors.map((f) => ({
          id: f.id,
          factor_type: 'totp',
          status: f.status,
          friendly_name: 'ryft-admin',
        })),
      });
    }
    if (s.path === '/auth/v1/factors' && s.method === 'POST') {
      // As GoTrue: with a verified factor, a new one needs an aal2 session (403 insufficient_aal,
      // the design's V-MFA-1); with max_enrolled_factors = 1 an aal2 session cannot add one either.
      const verified = this.factors.some((f) => f.status === 'verified');
      if (verified && info.aal !== 'aal2')
        return fail(403, 'insufficient_aal', 'AAL2 required to enroll a new factor');
      if (verified)
        return fail(
          422,
          'too_many_enrolled_mfa_factors',
          'Maximum number of verified factors reached, unenroll to continue',
        );
      if (this.factors.length > 0)
        return fail(422, 'mfa_factor_name_conflict', 'A factor with this name exists');
      const id = `factor-${++this.n}`;
      this.factors.push({ id, status: 'unverified' });
      return this.reply(route, 200, {
        id,
        type: 'totp',
        friendly_name: body.friendly_name,
        totp: {
          qr_code:
            // GoTrue's SVG writer starts with an XML declaration and a comment.
            '<?xml version="1.0"?>\n<!-- Generated by SVGo -->\n<svg xmlns="http://www.w3.org/2000/svg" width="200" height="200" viewBox="0 0 8 8"><rect width="8" height="8" fill="#fff"/><path d="M0 0h3v3H0zM5 0h3v3H5zM0 5h3v3H0z" fill="#000"/></svg>',
          secret: 'JBSWY3DPEHPK3PXP',
          uri: 'otpauth://totp/RYFT%20admin:admin@example.test?secret=JBSWY3DPEHPK3PXP&issuer=RYFT%20admin',
        },
      });
    }
    const factorMatch = /^\/auth\/v1\/factors\/([^/]+)(?:\/(challenge|verify))?$/.exec(s.path);
    if (factorMatch) {
      const [, id, action] = factorMatch;
      const factor = this.factors.find((f) => f.id === id);
      if (!factor) return fail(404, 'mfa_factor_not_found', 'MFA factor not found');
      if (!action && s.method === 'DELETE') {
        this.factors = this.factors.filter((f) => f.id !== id);
        return this.reply(route, 200, { id });
      }
      if (action === 'challenge') return this.reply(route, 200, { id: `challenge-${id}`, expires_at: 0 });
      if (action === 'verify') {
        if (body.code !== TOTP) return fail(400, 'mfa_verification_failed', 'Invalid TOTP code entered');
        factor.status = 'verified';
        if (!this.mfaSticky) this.mfaStale = false;
        return this.reply(route, 200, this.mint('aal2'));
      }
    }
    if (s.path === '/auth/v1/logout') return this.reply(route, 204);
    return fail(404, 'not_found', `no stub for ${s.method} ${s.path}`);
  }

  // ---- api.ryftapp.de --------------------------------------------------------------------
  private async api(route: Route) {
    if (route.request().method() === 'OPTIONS') return this.preflight(route);
    if (this.apiDown) return route.abort('failed');
    const s = this.record(route);
    const wait = this.delay.get(`${s.method} ${s.path}${s.search}`);
    if (wait) await new Promise((r) => setTimeout(r, wait));
    const bearer = s.authorization?.replace(/^Bearer /i, '') ?? null;
    const info = bearer ? this.tokens.get(bearer) : undefined;
    const err = (status: number, error: string, extra: Record<string, unknown> = {}) =>
      this.reply(route, status, { error, ...extra, request_id: `req-${this.n}` });

    if (!info || info.expired || info.role !== 'admin') {
      return this.reply(
        route,
        401,
        { error: 'admin key or admin sign-in required', request_id: `req-${this.n}` },
        { 'www-authenticate': 'Bearer realm="ryft-admin"' },
      );
    }
    if (info.aal !== 'aal2' || this.mfaStale) {
      return err(403, 'a second factor confirmed in the last 12 hours is required', { code: 'mfa_required' });
    }

    const failing = this.failNext.get(`${s.method} ${s.path}`);
    if (failing && failing.times > 0) {
      failing.times -= 1;
      return err(failing.status, 'the server could not do it');
    }
    const q = new URLSearchParams(s.search);
    const emails = q.get('emails') === '1';
    const m = s.method;
    const p = s.path;

    if (p === '/v1/admin/invites' && m === 'GET') {
      return this.reply(
        route,
        200,
        this.invites.map((i) => ({
          ...i,
          email: i.email && !emails && !i.email.includes('***') ? mask(i.email) : i.email,
          uses_by: i.uses_by.map((u) => ({
            ...u,
            email: u.email && !emails && !u.email.includes('***') ? mask(u.email) : u.email,
          })),
        })),
      );
    }
    if (p === '/v1/admin/invites' && m === 'POST') {
      const b = s.body as { note?: string; email?: string; max_uses?: number; expires_at?: string };
      const email = b.email ? b.email.trim().toLowerCase() : null;
      if (email && !/^[!-~]+@[!-~]+\.[!-~]+$/.test(email)) return err(422, 'email is not a valid address');
      if (email && (b.max_uses ?? 1) !== 1) return err(422, 'a code for one email has max_uses 1');
      if (b.expires_at && Date.parse(b.expires_at) <= Date.now())
        return err(422, 'expires_at must be in the future');
      // A bound code always expires: 14 days when omitted, at most 90 (docs/API.md).
      if (email && b.expires_at && Date.parse(b.expires_at) > Date.now() + 90 * DAY)
        return err(422, 'expires_at is at most 90 days away for a code for one email');
      const expires = b.expires_at ?? (email ? daysFromNow(14) : null);
      this.codes += 1;
      const code = `RYFT-7K3M-QX${String(this.codes).padStart(2, '0')}`;
      const inv: Invite = {
        code,
        note: b.note ?? '',
        max_uses: b.max_uses ?? 1,
        uses: 0,
        expires_at: expires,
        created_at: new Date().toISOString(),
        email,
        created_by: { kind: 'user', id: '1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081' },
        revoked_at: null,
        status: 'open',
        link: `https://ryftapp.de/invite/${code}`,
        uses_by: [],
      };
      this.invites.unshift(inv);
      return this.reply(route, 201, inv);
    }
    const invite = /^\/v1\/admin\/invites\/([^/]+)$/.exec(p);
    if (invite && m === 'DELETE') {
      const inv = this.invites.find((i) => i.code === invite[1]);
      if (!inv) return err(404, 'no such code');
      inv.status = 'revoked';
      inv.revoked_at ??= new Date().toISOString();
      return this.reply(route, 204);
    }
    if (p === '/v1/admin/users' && m === 'GET') {
      return this.reply(
        route,
        200,
        this.users.map(({ email, ...u }) => (emails ? { ...u, email } : u)),
      );
    }
    const access = /^\/v1\/admin\/users\/([^/]+)\/access$/.exec(p);
    if (access && (m === 'POST' || m === 'DELETE')) {
      const u = this.users.find((x) => x.id === access[1]);
      if (!u) return err(404, 'no such user');
      u.beta_access = m === 'POST';
      return this.reply(route, 204);
    }
    if (p === '/v1/admin/feedback' && m === 'GET') {
      // `since`: RFC 3339 or YYYY-MM-DD, default 7 days; newest first.
      const since = q.get('since');
      const from = since === null ? Date.now() - 7 * DAY : Date.parse(since);
      if (Number.isNaN(from)) return err(422, 'since must be RFC 3339 or YYYY-MM-DD');
      return this.reply(
        route,
        200,
        this.feedback
          .filter((f) => Date.parse(f.created_at) >= from)
          .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at)),
      );
    }
    const resolve = /^\/v1\/admin\/feedback\/(\d+)\/resolve$/.exec(p);
    if (resolve && m === 'POST') {
      const f = this.feedback.find((x) => x.id === Number(resolve[1]));
      if (!f) return err(404, 'no such feedback');
      f.resolved_at = new Date().toISOString();
      return this.reply(route, 204);
    }
    if (p === '/v1/admin/flags' && m === 'GET') {
      return this.reply(route, 200, [
        {
          key: 'train.import',
          enabled: true,
          rollout_pct: 0,
          allow_user_ids: ['9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a', '0a1b2c3d-4e5f-4061-8283-949a5b6c7d8e'],
          updated_at: '2026-10-26T12:00:00Z',
        },
      ]);
    }
    if (p === '/v1/admin/audit' && m === 'GET') {
      return this.reply(route, 200, [
        {
          id: 9142,
          at: '2026-11-05T18:02:44Z',
          key_fingerprint: '',
          actor: { kind: 'user', id: '1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081' },
          action: 'POST /v1/admin/invites',
          target_user: null,
          status: 201,
          request_id: '01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c',
          details: { code: 'RYFT-7K3M-QX9P' },
        },
        {
          id: 9141,
          at: '2026-11-05T17:58:03Z',
          key_fingerprint: '',
          actor: { kind: 'user', id: '1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081' },
          action: 'PUT /v1/admin/flags/{key}',
          target_user: null,
          status: 403,
          request_id: '01a14ff9-91c0-7a2e-b6d3-0f4e5a6b7c8d',
          details: {},
        },
      ]);
    }
    return err(404, 'not found');
  }
}

function mask(email: string) {
  const [local, domain] = email.split('@');
  const [host, ...rest] = domain!.split('.');
  return `${local![0]}***@${host![0]}***.${rest.join('.')}`;
}

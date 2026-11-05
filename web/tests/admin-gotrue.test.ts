import { describe, expect, it, vi } from 'vitest';
import { AuthError, createAuthClient, qrDataUrl } from '../src/admin/gotrue';
import { decodeClaims } from '../src/admin/jwt';

// The Supabase Auth REST calls of the admin page, against a mocked fetch. The shapes are the
// ones GoTrue sends; the e2e stub (e2e/admin-stub.ts) pins the same ones.
const CFG = { url: 'https://proj.supabase.co', key: 'sb_publishable_test' };
const NOW = Date.UTC(2026, 10, 5, 12, 0, 0);

type Call = { url: string; init: RequestInit };
function mock(...answers: (Response | Error)[]) {
  const calls: Call[] = [];
  const fn = vi.fn(async (url: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    const next = answers.shift();
    if (!next) throw new Error('unexpected call');
    if (next instanceof Error) throw next;
    return next;
  });
  return { fn: fn as unknown as typeof fetch, calls };
}
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const tokenAnswer = {
  access_token: 'a.b.c',
  refresh_token: 'r1',
  expires_in: 3600,
  expires_at: Math.floor(NOW / 1000) + 3600,
  token_type: 'bearer',
  user: { id: 'u1' },
};

describe('password sign-in', () => {
  it('posts the password grant with the publishable key and no cookies, cache or referrer', async () => {
    const { fn, calls } = mock(json(tokenAnswer));
    const auth = createAuthClient(CFG, { fetch: fn, now: () => NOW });
    const s = await auth.signInWithPassword('admin@example.test', 'pw');
    expect(s).toEqual({
      access_token: 'a.b.c',
      refresh_token: 'r1',
      expires_at: Math.floor(NOW / 1000) + 3600,
    });
    const { url, init } = calls[0]!;
    expect(url).toBe('https://proj.supabase.co/auth/v1/token?grant_type=password');
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('omit');
    expect(init.cache).toBe('no-store');
    expect(init.referrerPolicy).toBe('no-referrer');
    const headers = init.headers as Record<string, string>;
    expect(headers.apikey).toBe(CFG.key);
    expect(headers.Authorization).toBeUndefined();
    expect(JSON.parse(String(init.body))).toEqual({ email: 'admin@example.test', password: 'pw' });
  });

  it('computes expires_at from expires_in when GoTrue sends none', async () => {
    const { fn } = mock(json({ access_token: 'a.b.c', refresh_token: 'r', expires_in: 600 }));
    const s = await createAuthClient(CFG, { fetch: fn, now: () => NOW }).signInWithPassword('e', 'p');
    expect(s.expires_at).toBe(Math.floor(NOW / 1000) + 600);
  });

  it('says the same for a wrong email and a wrong password', async () => {
    const { fn } = mock(
      json({ code: 400, error_code: 'invalid_credentials', msg: 'Invalid login credentials' }, 400),
    );
    const err = await createAuthClient(CFG, { fetch: fn })
      .signInWithPassword('e', 'p')
      .catch((e) => e);
    expect(err).toBeInstanceOf(AuthError);
    expect(err).toMatchObject({
      status: 400,
      code: 'invalid_credentials',
      message: 'Email or password is wrong.',
    });
  });

  it('turns a missing answer into a network error, not a sign-out', async () => {
    const { fn } = mock(new TypeError('Failed to fetch'));
    const err = await createAuthClient(CFG, { fetch: fn })
      .signInWithPassword('e', 'p')
      .catch((e) => e);
    expect(err).toMatchObject({ status: 0, code: 'network' });
  });

  it('does not put a token or a password into an error message', async () => {
    const { fn } = mock(json({ msg: 'boom' }, 500));
    const err = await createAuthClient(CFG, { fetch: fn })
      .signInWithPassword('e', 'secret-pw')
      .catch((e) => e);
    expect(String(err.message)).not.toContain('secret-pw');
  });

  it('refuses an answer without tokens', async () => {
    const { fn } = mock(json({ user: {} }));
    await expect(createAuthClient(CFG, { fetch: fn }).signInWithPassword('e', 'p')).rejects.toMatchObject({
      code: 'bad_response',
    });
  });
});

describe('refresh', () => {
  it('posts the refresh token and returns the rotated session', async () => {
    const { fn, calls } = mock(json({ ...tokenAnswer, refresh_token: 'r2' }));
    const s = await createAuthClient(CFG, { fetch: fn, now: () => NOW }).refresh('r1');
    expect(calls[0]!.url).toBe('https://proj.supabase.co/auth/v1/token?grant_type=refresh_token');
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({ refresh_token: 'r1' });
    expect(s.refresh_token).toBe('r2');
  });
});

describe('the second factor', () => {
  it('reads the factors of the user with the access token as bearer', async () => {
    const { fn, calls } = mock(
      json({
        id: 'u1',
        factors: [
          { id: 'f1', factor_type: 'totp', status: 'verified', friendly_name: 'x' },
          { id: 'f2', factor_type: 'totp', status: 'unverified' },
          { factor_type: 'phone', status: 'verified' },
        ],
      }),
    );
    const u = await createAuthClient(CFG, { fetch: fn }).getUser('tok');
    expect(calls[0]!.url).toBe('https://proj.supabase.co/auth/v1/user');
    expect((calls[0]!.init.headers as Record<string, string>).Authorization).toBe('Bearer tok');
    expect(u.factors.map((f) => [f.id, f.status])).toEqual([
      ['f1', 'verified'],
      ['f2', 'unverified'],
    ]);
  });

  it('treats a user without factors as having none', async () => {
    const { fn } = mock(json({ id: 'u1' }));
    expect((await createAuthClient(CFG, { fetch: fn }).getUser('tok')).factors).toEqual([]);
  });

  it('enrols a TOTP factor and returns the QR code as a data URL, the secret and the URI', async () => {
    const svg = '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>';
    const { fn, calls } = mock(
      json({
        id: 'f9',
        type: 'totp',
        totp: { qr_code: svg, secret: 'JBSWY3DPEHPK3PXP', uri: 'otpauth://totp/x' },
      }),
    );
    const e = await createAuthClient(CFG, { fetch: fn }).enrolTotp('tok', 'ryft-admin 2026-11-05');
    expect(calls[0]!.url).toBe('https://proj.supabase.co/auth/v1/factors');
    expect(JSON.parse(String(calls[0]!.init.body))).toMatchObject({
      factor_type: 'totp',
      friendly_name: 'ryft-admin 2026-11-05',
    });
    expect(e).toMatchObject({ id: 'f9', secret: 'JBSWY3DPEHPK3PXP', uri: 'otpauth://totp/x' });
    expect(e.qr).toBe(`data:image/svg+xml;utf-8,${encodeURIComponent(svg)}`);
  });

  it('removes a leftover unverified factor', async () => {
    const { fn, calls } = mock(new Response(null, { status: 204 }));
    await createAuthClient(CFG, { fetch: fn }).unenroll('tok', 'f2');
    expect(calls[0]).toMatchObject({ url: 'https://proj.supabase.co/auth/v1/factors/f2' });
    expect(calls[0]!.init.method).toBe('DELETE');
  });

  it('challenges, then verifies with the challenge id and the code, and returns the aal2 session', async () => {
    const { fn, calls } = mock(
      json({ id: 'ch1', expires_at: 1 }),
      json({ ...tokenAnswer, refresh_token: 'r-aal2' }),
    );
    const auth = createAuthClient(CFG, { fetch: fn, now: () => NOW });
    const ch = await auth.challenge('tok', 'f1');
    expect(ch).toBe('ch1');
    expect(calls[0]!.url).toBe('https://proj.supabase.co/auth/v1/factors/f1/challenge');
    const s = await auth.verify('tok', 'f1', ch, '123456');
    expect(calls[1]!.url).toBe('https://proj.supabase.co/auth/v1/factors/f1/verify');
    expect(JSON.parse(String(calls[1]!.init.body))).toEqual({ challenge_id: 'ch1', code: '123456' });
    expect(s.refresh_token).toBe('r-aal2');
  });

  it('maps a wrong code to a plain message', async () => {
    const { fn } = mock(
      json({ code: 400, error_code: 'mfa_verification_failed', msg: 'Invalid TOTP code entered' }, 400),
    );
    const err = await createAuthClient(CFG, { fetch: fn })
      .verify('tok', 'f1', 'ch1', '000000')
      .catch((e) => e);
    expect(err).toMatchObject({ status: 400, code: 'mfa_verification_failed' });
    expect(err.message).toMatch(/wrong or has expired/);
  });
});

describe('sign out', () => {
  it('ends every session of the account', async () => {
    const { fn, calls } = mock(new Response(null, { status: 204 }));
    await createAuthClient(CFG, { fetch: fn }).signOut('tok');
    expect(calls[0]!.url).toBe('https://proj.supabase.co/auth/v1/logout?scope=global');
    expect(calls[0]!.init.method).toBe('POST');
    expect((calls[0]!.init.headers as Record<string, string>).Authorization).toBe('Bearer tok');
  });
});

describe('qrDataUrl', () => {
  it('accepts SVG markup or a data URL and nothing else', () => {
    expect(qrDataUrl('<svg></svg>')).toMatch(/^data:image\/svg\+xml;utf-8,%3Csvg/);
    expect(qrDataUrl('data:image/svg+xml;utf-8,<svg></svg>')).toMatch(/^data:image\/svg\+xml;utf-8,%3Csvg/);
    expect(qrDataUrl('data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=')).toBe(
      'data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=',
    );
    expect(qrDataUrl('javascript:alert(1)')).toBeNull();
    expect(qrDataUrl('https://example.test/qr.svg')).toBeNull();
    expect(qrDataUrl('data:text/html,<script>alert(1)</script>')).toBeNull();
    expect(qrDataUrl(undefined)).toBeNull();
  });
});

describe('decodeClaims', () => {
  const b64 = (o: unknown) => Buffer.from(JSON.stringify(o)).toString('base64url');
  const token = (payload: unknown) => `${b64({ alg: 'ES256' })}.${b64(payload)}.sig`;

  it('reads aal and app_metadata.role, and nothing from user_metadata or the top-level role', () => {
    expect(decodeClaims(token({ aal: 'aal2', app_metadata: { role: 'admin' }, exp: 5 }))).toEqual({
      aal: 'aal2',
      appRole: 'admin',
      exp: 5,
    });
    expect(decodeClaims(token({ aal: 'aal1', role: 'admin', user_metadata: { role: 'admin' } }))).toEqual({
      aal: 'aal1',
      appRole: null,
      exp: null,
    });
    expect(decodeClaims(token({ aal: 2, app_metadata: { role: ['admin'] } }))).toEqual({
      aal: null,
      appRole: null,
      exp: null,
    });
  });

  it('survives garbage', () => {
    for (const t of ['', 'a.b', 'a.b.c', 'a.!!!.c', `x.${Buffer.from('[1]').toString('base64url')}.y`]) {
      expect(() => decodeClaims(t)).not.toThrow();
    }
    expect(decodeClaims('a.b')).toBeNull();
  });
});

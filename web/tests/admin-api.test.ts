import { describe, expect, it, vi } from 'vitest';
import { ApiError, SessionEnded, createApi, describeError } from '../src/admin/api';
import { AuthError } from '../src/admin/gotrue';
import { REFRESH_MARGIN_MS, createSessions } from '../src/admin/sessions';
import { SESSION_KEY, clearSession, loadSession, saveSession, type KV } from '../src/admin/storage';
import type { Session } from '../src/admin/types';

const NOW = Date.UTC(2026, 10, 5, 12, 0, 0);
const secs = (ms: number) => Math.floor(ms / 1000);

function memory(): KV & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
    removeItem: (k) => void data.delete(k),
  };
}

const session = (over: Partial<Session> = {}): Session => ({
  access_token: 'access-1',
  refresh_token: 'refresh-1',
  expires_at: secs(NOW) + 3600,
  ...over,
});

describe('session storage', () => {
  it('keeps the session under one key and nothing else', () => {
    const kv = memory();
    saveSession(kv, session());
    expect([...kv.data.keys()]).toEqual([SESSION_KEY]);
    expect(loadSession(kv)).toEqual(session());
    clearSession(kv);
    expect(loadSession(kv)).toBeNull();
  });

  it('ignores a stored value of the wrong shape and survives a blocked storage', () => {
    const kv = memory();
    for (const bad of ['', 'nope', '[]', '{"access_token":1}', '{"access_token":"a","refresh_token":"b"}']) {
      kv.data.set(SESSION_KEY, bad);
      expect(loadSession(kv)).toBeNull();
    }
    const blocked: KV = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('blocked');
      },
      removeItem: () => {
        throw new Error('blocked');
      },
    };
    expect(loadSession(blocked)).toBeNull();
    expect(() => saveSession(blocked, session())).not.toThrow();
    expect(() => clearSession(blocked)).not.toThrow();
  });
});

describe('sessions', () => {
  it('refreshes before the access token expires (60 s margin), not earlier', async () => {
    // Pinned: the margin is a choice (longer than any clock skew we expect, far below 1 h).
    expect(REFRESH_MARGIN_MS).toBe(60_000);
    const kv = memory();
    const refresh = vi.fn(async () => session({ access_token: 'access-2', refresh_token: 'refresh-2' }));
    let now = NOW;
    const s = createSessions({ refresh }, kv, () => now);

    saveSession(kv, session({ expires_at: secs(NOW) + 3600 }));
    expect((await s.fresh())?.access_token).toBe('access-1');
    expect(refresh).not.toHaveBeenCalled();

    now = NOW + 3600_000 - 61_000; // 61 s left
    expect((await s.fresh())?.access_token).toBe('access-1');
    expect(refresh).not.toHaveBeenCalled();

    now = NOW + 3600_000 - 59_000; // 59 s left
    expect((await s.fresh())?.access_token).toBe('access-2');
    expect(refresh).toHaveBeenCalledWith('refresh-1');
    expect(loadSession(kv)?.refresh_token).toBe('refresh-2');
  });

  it('runs one refresh at a time (refresh tokens are single-use)', async () => {
    const kv = memory();
    saveSession(kv, session({ expires_at: secs(NOW) - 10 }));
    const refresh = vi.fn(async () => {
      await new Promise((r) => setTimeout(r, 5));
      return session({ access_token: 'access-2' });
    });
    const s = createSessions({ refresh }, kv, () => NOW);
    const [a, b] = await Promise.all([s.fresh(), s.fresh()]);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(a?.access_token).toBe('access-2');
    expect(b?.access_token).toBe('access-2');
  });

  it('clears the session when the refresh token is refused, and keeps it when there was no answer', async () => {
    const kv = memory();
    saveSession(kv, session({ expires_at: secs(NOW) - 10 }));
    const refused = createSessions(
      {
        refresh: async () =>
          Promise.reject(new AuthError('Invalid Refresh Token', 400, 'refresh_token_not_found')),
      },
      kv,
      () => NOW,
    );
    expect(await refused.fresh()).toBeNull();
    expect(loadSession(kv)).toBeNull();

    saveSession(kv, session({ expires_at: secs(NOW) - 10 }));
    const offline = createSessions(
      { refresh: async () => Promise.reject(new AuthError('no answer', 0, 'network')) },
      kv,
      () => NOW,
    );
    await expect(offline.fresh()).rejects.toMatchObject({ status: 0 });
    expect(loadSession(kv)).not.toBeNull();
  });

  it.each([400, 401, 403])('a refusal (%i) ends the session', async (status) => {
    const kv = memory();
    saveSession(kv, session({ expires_at: secs(NOW) - 10 }));
    const s = createSessions(
      { refresh: async () => Promise.reject(new AuthError('refused', status, null)) },
      kv,
      () => NOW,
    );
    expect(await s.refresh()).toBeNull();
    expect(loadSession(kv)).toBeNull();
  });

  it.each([429, 500, 502, 503, 504])(
    'a rate limit or server error (%i) keeps the session and throws',
    async (status) => {
      const kv = memory();
      saveSession(kv, session({ expires_at: secs(NOW) - 10 }));
      const s = createSessions(
        { refresh: async () => Promise.reject(new AuthError('busy', status, null)) },
        kv,
        () => NOW,
      );
      await expect(s.refresh()).rejects.toMatchObject({ status });
      expect(loadSession(kv)?.refresh_token).toBe('refresh-1');
    },
  );
});

function setup(
  responses: (Response | Error)[],
  opts: { refresh?: () => Promise<Session>; revoke?: (token: string) => Promise<void> } = {},
) {
  const kv = memory();
  saveSession(kv, session());
  const refresh = vi.fn(
    opts.refresh ?? (async () => session({ access_token: 'access-2', refresh_token: 'refresh-2' })),
  );
  const sessions = createSessions({ refresh }, kv, () => NOW);
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchFn = (async (url: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    const next = responses.shift();
    if (!next) throw new Error('unexpected call');
    if (next instanceof Error) throw next;
    return next;
  }) as unknown as typeof fetch;
  const onOk = vi.fn();
  const revoke = vi.fn(opts.revoke ?? (async () => undefined));
  const api = createApi({ baseUrl: 'https://api.example.test', sessions, fetch: fetchFn, onOk, revoke });
  return { api, calls, refresh, kv, onOk, revoke };
}
const json = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

describe('the admin api client', () => {
  it('sends the bearer with no cookies, cache or referrer, and returns the JSON', async () => {
    const { api, calls, onOk } = setup([json([{ code: 'RYFT-AAAA-BBBB' }])]);
    expect(await api.get('/v1/admin/invites?emails=1')).toEqual([{ code: 'RYFT-AAAA-BBBB' }]);
    const { url, init } = calls[0]!;
    expect(url).toBe('https://api.example.test/v1/admin/invites?emails=1');
    expect(init.credentials).toBe('omit');
    expect(init.cache).toBe('no-store');
    expect(init.referrerPolicy).toBe('no-referrer');
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer access-1');
    expect(onOk).toHaveBeenCalledOnce();
  });

  it('answers a 204 with nothing and sends a JSON body with its content type', async () => {
    const { api, calls } = setup([new Response(null, { status: 204 })]);
    expect(await api.post('/v1/admin/feedback/42/resolve', { a: 1 })).toBeUndefined();
    expect((calls[0]!.init.headers as Record<string, string>)['Content-Type']).toBe('application/json');
    expect(calls[0]!.init.body).toBe('{"a":1}');
  });

  it('on a 401 refreshes once and sends the same request again with the new token', async () => {
    const { api, calls, refresh } = setup([json({ error: 'x' }, 401), json([])]);
    expect(await api.get('/v1/admin/users')).toEqual([]);
    expect(refresh).toHaveBeenCalledOnce();
    expect(calls).toHaveLength(2);
    expect((calls[1]!.init.headers as Record<string, string>).Authorization).toBe('Bearer access-2');
  });

  it('on a second 401 ends the session: cleared storage, SessionEnded, no third request', async () => {
    const { api, calls, kv, revoke } = setup([json({ error: 'x' }, 401), json({ error: 'x' }, 401)]);
    await expect(api.get('/v1/admin/users')).rejects.toBeInstanceOf(SessionEnded);
    expect(calls).toHaveLength(2);
    expect(loadSession(kv)).toBeNull();
    // ... and revokes the refreshed session's refresh token (GoTrue logout), best effort.
    await vi.waitFor(() => expect(revoke).toHaveBeenCalledWith('access-2'));
  });

  it('a failing revoke after a second 401 changes nothing', async () => {
    const { api, kv, revoke } = setup([json({ error: 'x' }, 401), json({ error: 'x' }, 401)], {
      revoke: () => Promise.reject(new AuthError('no answer', 0, 'network')),
    });
    await expect(api.get('/v1/admin/users')).rejects.toBeInstanceOf(SessionEnded);
    await vi.waitFor(() => expect(revoke).toHaveBeenCalledOnce());
    expect(loadSession(kv)).toBeNull();
  });

  it('a refresh the sign-in service could not decide (5xx, 429) keeps the session: no answer, try again', async () => {
    for (const status of [503, 429]) {
      const { api, kv, revoke } = setup([json({ error: 'x' }, 401)], {
        refresh: () => Promise.reject(new AuthError('busy', status, null)),
      });
      const err = await api.get('/v1/admin/users').catch((e) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect(err).toMatchObject({ network: true, status: 0 });
      expect(describeError(err)).toMatch(/You are still signed in; try again/);
      expect(loadSession(kv)?.refresh_token).toBe('refresh-1');
      expect(revoke).not.toHaveBeenCalled();
    }
  });

  it('ends the session when the refresh token is refused, and when there is no session', async () => {
    const refused = setup([json({ error: 'x' }, 401)], {
      refresh: () => Promise.reject(new AuthError('Invalid Refresh Token', 400, null)),
    });
    await expect(refused.api.get('/v1/admin/users')).rejects.toBeInstanceOf(SessionEnded);

    const none = setup([]);
    clearSession(none.kv);
    await expect(none.api.get('/v1/admin/users')).rejects.toBeInstanceOf(SessionEnded);
    expect(none.calls).toHaveLength(0);
  });

  it('hands mfa_required to the caller with its code and request id', async () => {
    const { api } = setup([
      json({ error: 'a second factor …', code: 'mfa_required', request_id: 'req-1' }, 403, {
        'X-Request-ID': 'req-h',
      }),
    ]);
    const err = await api.get('/v1/admin/invites').catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 403, code: 'mfa_required', requestId: 'req-1', network: false });
  });

  it('takes the request id from the header when the body has none, and Retry-After from a 429', async () => {
    const { api } = setup([
      json({ error: 'too many requests' }, 429, { 'X-Request-ID': 'req-h', 'Retry-After': '3' }),
    ]);
    const err = await api.get('/v1/admin/invites').catch((e) => e);
    expect(err).toMatchObject({ status: 429, requestId: 'req-h', retryAfter: 3 });
    expect(describeError(err)).toBe('too many requests. Try again in 3 s. (429, request id req-h)');
  });

  it('a 429 without Retry-After still says when (1 s); a full stop is not doubled', async () => {
    const { api } = setup([json({ error: 'too many requests' }, 429, { 'X-Request-ID': 'req-h' })]);
    const err = await api.get('/v1/admin/invites').catch((e) => e);
    expect(err).toMatchObject({ status: 429, retryAfter: null });
    expect(describeError(err)).toBe('too many requests. Try again in 1 s. (429, request id req-h)');
    expect(describeError(new ApiError('Slow down.', 503, null, null, 5))).toBe(
      'Slow down. Try again in 5 s. (503)',
    );
    expect(describeError(new ApiError('no such code', 404, null, 'r1', null))).toBe(
      'no such code (404, request id r1)',
    );
  });

  it('reports a missing answer (no CORS headers, an edge 429, a 502) without a request id', async () => {
    const { api } = setup([new TypeError('Failed to fetch')]);
    const err = await api.get('/v1/admin/invites').catch((e) => e);
    expect(err).toMatchObject({ network: true, status: 0, requestId: null });
    expect(describeError(err)).toBe('No answer from api.ryftapp.de (network or edge limit). Try again.');
  });

  it('writes nothing to the console (tokens are never logged)', async () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m));
    const { api } = setup([json({ error: 'x' }, 401), json({ error: 'x' }, 401)]);
    await api.get('/v1/admin/users').catch(() => undefined);
    for (const s of spies) expect(s).not.toHaveBeenCalled();
    spies.forEach((s) => s.mockRestore());
  });
});

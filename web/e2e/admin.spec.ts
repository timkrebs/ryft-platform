import { createHash } from 'node:crypto';
import AxeBuilder from '@axe-core/playwright';
import { expect, test, type Page } from '@playwright/test';
import { API, AUTH, EMAIL, PASSWORD, Stub, TOTP } from './admin-stub';

// The operator's page, against the real nginx image (headers, CSP) with Supabase Auth and
// the api replaced by e2e/admin-stub.ts. See web D15–D17.

const SCREEN_TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'];

/**
 * Console errors and uncaught exceptions (CSP and Trusted Types violations show up here). The
 * browser's own line for a 4xx answer ("Failed to load resource") is not one: the tests that
 * send a wrong password or code expect it.
 */
function watch(page: Page) {
  const errors: string[] = [];
  page.on(
    'console',
    (m) =>
      m.type() === 'error' &&
      !/^Failed to load resource: the server responded with a status of 4\d\d/.test(m.text()) &&
      errors.push(m.text()),
  );
  page.on('pageerror', (e) => errors.push(String(e)));
  return errors;
}

async function setup(page: Page, configure: (s: Stub) => void = () => undefined) {
  const stub = new Stub();
  configure(stub);
  await stub.install(page);
  return stub;
}

async function password(page: Page) {
  await page.goto('/admin');
  await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
  await page.getByLabel('Email', { exact: true }).fill(EMAIL);
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
  await page.getByRole('button', { name: 'Continue' }).click();
}

/** Sign in all the way to the sections (enrolling when the stub has no factor yet). */
async function signIn(page: Page, stub: Stub) {
  await password(page);
  if (stub.factors.some((f) => f.status === 'verified')) {
    await expect(page.getByRole('heading', { level: 1, name: 'Enter your code' })).toBeVisible();
    await page.getByLabel('6-digit code').fill(TOTP);
    await page.getByRole('button', { name: 'Verify' }).click();
  } else {
    await expect(page.getByRole('heading', { level: 1, name: 'Set up your authenticator' })).toBeVisible();
    await page.getByLabel('6-digit code').fill(TOTP);
    await page.getByRole('button', { name: 'Verify and continue' }).click();
  }
  await expect(page.getByRole('heading', { level: 1, name: 'Invites' })).toBeVisible();
}

const tab = (page: Page, name: string) =>
  page.getByRole('navigation', { name: 'Sections' }).getByRole('link', { name });

async function serious(page: Page) {
  const axe = await new AxeBuilder({ page }).withTags(SCREEN_TAGS).analyze();
  return axe.violations.map(
    (v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`,
  );
}

// ---------------------------------------------------------------------------------------------
test.describe('headers: /admin has its own policy, every other page keeps connect-src none', () => {
  const COMMON: Record<string, string | RegExp> = {
    'x-content-type-options': 'nosniff',
    'referrer-policy': 'no-referrer',
    'x-frame-options': 'DENY',
    'strict-transport-security': /max-age=31536000/,
    'cross-origin-opener-policy': 'same-origin',
    'cross-origin-resource-policy': 'same-origin',
    'permissions-policy': /camera=\(\)/,
  };
  const directive = (csp: string, name: string) =>
    csp
      .split('; ')
      .find((d) => d.startsWith(`${name} `))
      ?.slice(name.length + 1);

  test('/admin sends the admin policy, noindex and no-store, plus every shared header', async ({
    request,
  }) => {
    const res = await request.get('/admin', { maxRedirects: 0 });
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('text/html');
    const csps = res.headersArray().filter((h) => h.name.toLowerCase() === 'content-security-policy');
    expect(csps, 'exactly one Content-Security-Policy').toHaveLength(1);
    const csp = csps[0]!.value;
    expect(directive(csp, 'connect-src')).toBe(`${API} ${AUTH}`);
    expect(directive(csp, 'trusted-types')).toBe("'none'");
    expect(directive(csp, 'require-trusted-types-for')).toBe("'script'");
    expect(directive(csp, 'frame-ancestors')).toBe("'none'");
    expect(directive(csp, 'form-action')).toBe("'none'");
    expect(directive(csp, 'img-src')).toBe("'self' data:");
    expect(directive(csp, 'script-src')).toMatch(/^'sha256-[^']+'$/);
    expect(csp).not.toContain('unsafe-');
    const h = res.headers();
    expect(h['x-robots-tag']).toBe('noindex, nofollow');
    expect(h['cache-control']).toBe('no-store');
    expect(h['set-cookie']).toBeUndefined();
    for (const [name, want] of Object.entries(COMMON)) {
      if (typeof want === 'string') expect(h[name], name).toBe(want);
      else expect(h[name], name).toMatch(want);
    }
    // The hash in the header is the hash of the one script in the page.
    const body = await res.text();
    const scripts = [...body.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)];
    expect(scripts).toHaveLength(1);
    const hash = `'sha256-${createHash('sha256').update(scripts[0]![1]!, 'utf8').digest('base64')}'`;
    expect(directive(csp, 'script-src')).toBe(hash);
    // Share and clipboard-write stay allowed (the Permissions-Policy lists neither).
    expect(h['permissions-policy']).not.toMatch(/web-share|clipboard-write/);
  });

  test('every other page sends the marketing policy: connect-src none, no admin hash, no Supabase', async ({
    request,
  }) => {
    const admin = await request.get('/admin');
    const adminHash = directive(admin.headers()['content-security-policy']!, 'script-src');
    for (const path of [
      '/',
      '/privacy',
      '/beta',
      '/de',
      '/invite/TEST',
      '/auth/callback',
      '/no-such-page',
      '/admin/x',
    ]) {
      const res = await request.get(path, { maxRedirects: 0 });
      const csps = res.headersArray().filter((h) => h.name.toLowerCase() === 'content-security-policy');
      expect(csps, `${path}: one policy`).toHaveLength(1);
      const csp = csps[0]!.value;
      expect(directive(csp, 'connect-src'), path).toBe("'none'");
      expect(directive(csp, 'img-src'), path).toBe("'self'");
      expect(csp, path).not.toContain('trusted-types ');
      expect(csp, path).not.toContain('supabase');
      expect(csp, path).not.toContain(adminHash!);
      expect(res.headers()['x-robots-tag'], path).toBeUndefined();
      for (const [name, want] of Object.entries(COMMON)) {
        if (typeof want === 'string') expect(res.headers()[name], `${path} ${name}`).toBe(want);
        else expect(res.headers()[name], `${path} ${name}`).toMatch(want);
      }
    }
  });

  test('/admin.html and /admin/ redirect to /admin; the file is never served under the marketing policy', async ({
    request,
  }) => {
    for (const from of ['/admin.html', '/admin/']) {
      const res = await request.get(from, { maxRedirects: 0 });
      expect(res.status(), from).toBe(301);
      expect(res.headers()['location'], from).toBe('/admin');
    }
  });

  test('a marketing page cannot reach the api or Supabase (connect-src none)', async ({ page }) => {
    await page.goto('/privacy');
    const outcome = await page.evaluate(
      async ([api, auth]) => {
        const violations: string[] = [];
        document.addEventListener('securitypolicyviolation', (e) => violations.push(e.effectiveDirective));
        const tried = await Promise.all(
          [`${api}/v1/config`, `${auth}/auth/v1/health`].map((u) =>
            fetch(u, { mode: 'no-cors' }).then(
              () => 'ok',
              () => 'blocked',
            ),
          ),
        );
        await new Promise((r) => setTimeout(r, 50));
        return { tried, violations };
      },
      [API, AUTH],
    );
    expect(outcome.tried).toEqual(['blocked', 'blocked']);
    expect(outcome.violations).toEqual(['connect-src', 'connect-src']);
  });

  test('/admin reaches its two origins and nothing else; no third-party request, no cookie', async ({
    page,
    context,
  }) => {
    const foreign: string[] = [];
    page.on('request', (r) => {
      const u = new URL(r.url());
      if (!['localhost', '127.0.0.1'].includes(u.hostname)) foreign.push(u.origin);
    });
    const stub = await setup(page);
    await signIn(page, stub);
    const blocked = await page.evaluate(async () => {
      const violations: string[] = [];
      document.addEventListener('securitypolicyviolation', (e) => violations.push(e.blockedURI));
      const r = await fetch('https://example.com/x', { mode: 'no-cors' }).then(
        () => 'ok',
        () => 'blocked',
      );
      await new Promise((res) => setTimeout(res, 50));
      return { r, violations };
    });
    expect(blocked.r).toBe('blocked');
    expect(blocked.violations.length).toBe(1);
    expect(new Set(foreign)).toEqual(new Set([API, AUTH]));
    expect(await context.cookies()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------------------------
test.describe('sign in', () => {
  test('first use: password, then enrol the authenticator (QR + secret), then the sections', async ({
    page,
  }) => {
    const errors = watch(page);
    const stub = await setup(page);
    await password(page);
    await expect(page.getByRole('heading', { level: 1, name: 'Set up your authenticator' })).toBeVisible();
    await expect(page.getByTestId('totp-secret')).toHaveText('JBSWY3DPEHPK3PXP');
    // The QR code is an SVG data URL: it loads under img-src data: and shows.
    await expect
      .poll(() =>
        page.locator('img.adm-qr').evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0),
      )
      .toBe(true);
    await page.getByLabel('6-digit code').fill('000000');
    await page.getByRole('button', { name: 'Verify and continue' }).click();
    await expect(page.getByRole('alert')).toContainText('wrong or has expired');
    await page.getByLabel('6-digit code').fill(TOTP);
    await page.getByRole('button', { name: 'Verify and continue' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Invites' })).toBeVisible();
    expect(errors).toEqual([]);
    // The calls, in order: the GoTrue flow, then the api with the aal2 token.
    const flow = stub.seen
      .filter((s) => s.path.startsWith('/auth/v1'))
      .map((s) => `${s.method} ${s.path}${s.search}`);
    expect(flow.slice(0, 3)).toEqual([
      'POST /auth/v1/token?grant_type=password',
      'GET /auth/v1/user',
      'POST /auth/v1/factors',
    ]);
    expect(flow.filter((f) => f.endsWith('/verify'))).toHaveLength(2);
    expect(stub.seen.find((s) => s.path === '/v1/admin/invites')?.authorization).toMatch(/^Bearer ey/);
  });

  test('afterwards: password, then the code, no enrolment', async ({ page }) => {
    const stub = await setup(page, (s) => (s.factors = [{ id: 'factor-existing', status: 'verified' }]));
    await signIn(page, stub);
    expect(stub.count('POST', '/auth/v1/factors')).toBe(0);
    expect(stub.count('POST', '/auth/v1/factors/factor-existing/challenge')).toBe(1);
  });

  test('a wrong password says so, once, without telling email from password', async ({ page }) => {
    await setup(page);
    await page.goto('/admin');
    await page.getByLabel('Email', { exact: true }).fill(EMAIL);
    await page.getByLabel('Password', { exact: true }).fill('nope');
    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page.getByRole('alert')).toHaveText('Email or password is wrong.');
    await expect(page.getByLabel('Password', { exact: true })).toBeVisible();
  });

  test('an account without the operator role ends at once: signed out, told so', async ({ page }) => {
    const stub = await setup(page, (s) => (s.role = null));
    await password(page);
    await page.getByLabel('6-digit code').fill(TOTP);
    await page.getByRole('button', { name: 'Verify and continue' }).click();
    await expect(page.getByRole('status').filter({ hasText: 'not an operator account' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
    expect(stub.count('POST', '/auth/v1/logout')).toBe(1);
    expect(stub.seen.some((s) => s.path.startsWith('/v1/admin'))).toBe(false);
    expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
  });

  test('the session is in sessionStorage only: no localStorage, no cookie, and a reload keeps it', async ({
    page,
    context,
  }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    const stored = await page.evaluate(() => ({
      session: Object.keys(sessionStorage),
      local: localStorage.length,
    }));
    expect(stored).toEqual({ session: ['ryft.admin.session'], local: 0 });
    expect(await context.cookies()).toEqual([]);
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Invites' })).toBeVisible();
    expect(stub.count('POST', '/auth/v1/factors')).toBe(1); // no second enrolment
  });

  test('Sign out ends every session of the account and clears the tab', async ({ page }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
    const logout = stub.seen.find((s) => s.path === '/auth/v1/logout');
    expect(logout?.search).toBe('?scope=global');
    expect(logout?.authorization).toMatch(/^Bearer ey/);
    expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
    await page.reload();
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
  });
});

// ---------------------------------------------------------------------------------------------
test.describe('the api answers', () => {
  test('401: refresh once and send again', async ({ page }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    stub.expireAccessTokens();
    await tab(page, 'Users').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Users' })).toBeVisible();
    await expect(page.getByText('Account 9f8e7d6c')).toBeVisible();
    expect(stub.seen.filter((s) => s.search === '?grant_type=refresh_token')).toHaveLength(1);
    expect(stub.count('GET', '/v1/admin/users')).toBe(2);
  });

  test('a second 401 means sign in again', async ({ page }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    stub.expireAccessTokens();
    stub.role = null; // the refreshed token no longer carries the role
    await tab(page, 'Users').click();
    await expect(page.getByRole('status').filter({ hasText: 'Your session ended' })).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
    expect(await page.evaluate(() => sessionStorage.length)).toBe(0);
  });

  test('403 mfa_required: the code prompt, then the same screen again', async ({ page }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    stub.mfaStale = true;
    await tab(page, 'Users').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Enter your code' })).toBeVisible();
    await expect(page.getByText('Confirm with your code to continue.')).toBeVisible();
    await page.getByLabel('6-digit code').fill(TOTP);
    await page.getByRole('button', { name: 'Verify' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Users' })).toBeVisible();
    await expect(page.getByText('Account 9f8e7d6c')).toBeVisible();
  });

  test('403 mfa_required that a code does not fix: sign in again', async ({ page }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    stub.mfaStale = true;
    stub.mfaSticky = true;
    await tab(page, 'Feedback').click();
    await expect(page.getByRole('heading', { level: 1, name: 'Enter your code' })).toBeVisible();
    await page.getByLabel('6-digit code').fill(TOTP);
    await page.getByRole('button', { name: 'Verify' }).click();
    await expect(
      page.getByRole('status').filter({ hasText: 'Sign in again to confirm with your code.' }),
    ).toBeVisible();
    await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
  });

  test('no answer at all (no CORS headers, an edge limit, a 502): says so, keeps the session', async ({
    page,
  }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    stub.apiDown = true;
    await tab(page, 'Flags').click();
    await expect(page.getByRole('alert')).toHaveText(
      'No answer from api.ryftapp.de (network or edge limit). Try again.',
    );
    stub.apiDown = false;
    await tab(page, 'Invites').click();
    await expect(page.getByText('RYFT-H2NP-4WCX')).toBeVisible();
  });

  test('calls only the routes an operator token may reach', async ({ page }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    for (const name of ['Users', 'Feedback', 'Flags', 'Audit']) {
      await tab(page, name).click();
      await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
    }
    const calls = new Set(
      stub.seen.filter((s) => s.path.startsWith('/v1/admin')).map((s) => `${s.method} ${s.path}`),
    );
    expect(calls).toEqual(
      new Set([
        'GET /v1/admin/invites',
        'GET /v1/admin/users',
        'GET /v1/admin/feedback',
        'GET /v1/admin/flags',
        'GET /v1/admin/audit',
      ]),
    );
  });
});

// ---------------------------------------------------------------------------------------------
test.describe('invites', () => {
  test('create a code for one email: the code, the link, Copy, Share; the list; revoke', async ({
    page,
    context,
  }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.addInitScript(() => {
      (navigator as unknown as { share: (d: unknown) => Promise<void> }).share = async (d) => {
        (window as unknown as { __shared: unknown }).__shared = d;
      };
    });
    const errors = watch(page);
    const stub = await setup(page);
    await signIn(page, stub);

    await page.getByLabel('Email (optional)').fill('Anna@Example.com');
    await expect(page.getByLabel('Uses')).toBeDisabled();
    await page.getByLabel('Note (optional)').fill('Anna');
    await page.getByRole('button', { name: 'Create invite' }).click();

    const card = page.locator('.adm-new');
    await expect(card.getByTestId('new-code')).toHaveText(/^RYFT-7K3M-QX\d\d$/);
    const code = (await card.getByTestId('new-code').textContent())!;
    await expect(card.getByTestId('new-link')).toHaveText(`https://ryftapp.de/invite/${code}`);
    await expect(card).toContainText('Only for anna@example.com');

    const post = stub.seen.find((s) => s.method === 'POST' && s.path === '/v1/admin/invites')!;
    expect(post.body).toMatchObject({ email: 'Anna@Example.com', note: 'Anna', max_uses: 1 });
    const expires = new Date((post.body as { expires_at: string }).expires_at).getTime();
    expect(expires - Date.now()).toBeGreaterThan(13.9 * 86_400_000);
    expect(expires - Date.now()).toBeLessThan(14.1 * 86_400_000);
    expect((post.body as { expires_at: string }).expires_at).toMatch(/Z$/);

    await card.getByRole('button', { name: 'Copy link' }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
      `https://ryftapp.de/invite/${code}`,
    );
    await card.getByRole('button', { name: 'Copy code' }).click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(code);
    await card.getByRole('button', { name: 'Share' }).click();
    expect(await page.evaluate(() => (window as unknown as { __shared: unknown }).__shared)).toEqual({
      title: 'RYFT beta invite',
      text: `Your RYFT invite code: ${code}`,
      url: `https://ryftapp.de/invite/${code}`,
    });

    // The list shows it open, and the stub masks the address until the toggle asks for it.
    await expect(page.locator('ul.adm-cards > li', { hasText: code })).toContainText('open');
    expect(stub.seen.filter((s) => s.method === 'GET' && s.path === '/v1/admin/invites').at(-1)?.search).toBe(
      '',
    );
    await page.getByLabel('Show email addresses').check();
    await expect
      .poll(
        () => stub.seen.filter((s) => s.method === 'GET' && s.path === '/v1/admin/invites').at(-1)?.search,
      )
      .toBe('?emails=1');
    await expect(page.locator('ul.adm-cards > li', { hasText: code })).toContainText('anna@example.com');

    // Revoke: asks first, then the row says revoked.
    const open = page.locator('ul.adm-cards > li', { hasText: code });
    await open.getByRole('button', { name: 'Revoke' }).click();
    await open.getByRole('button', { name: 'Revoke now' }).click();
    await expect(open.getByText('revoked', { exact: true })).toBeVisible();
    expect(stub.count('DELETE', `/v1/admin/invites/${code}`)).toBe(1);
    await expect(open.getByRole('button', { name: 'Revoke' })).toHaveCount(0);
    expect(errors).toEqual([]);
  });

  test('without navigator.share there is no Share button and Copy stays', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.addInitScript(() => {
      delete (Navigator.prototype as unknown as { share?: unknown }).share;
    });
    const stub = await setup(page);
    await signIn(page, stub);
    await page.getByRole('button', { name: 'Create invite' }).click();
    const card = page.locator('.adm-new');
    await expect(card.getByTestId('new-code')).toBeVisible();
    await expect(card.getByRole('button', { name: 'Share' })).toHaveCount(0);
    await expect(card.getByRole('button', { name: 'Copy link' })).toBeVisible();
    // No email: an unbound code with the page's defaults (1 use, 14 days).
    const post = stub.seen.find((s) => s.method === 'POST' && s.path === '/v1/admin/invites')!;
    expect(post.body).toMatchObject({ max_uses: 1 });
    expect(post.body).not.toHaveProperty('email');
  });

  test('a server refusal is shown with its request id', async ({ page }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    await page.getByLabel('Email (optional)').fill('not an address');
    await page.getByRole('button', { name: 'Create invite' }).click();
    await expect(page.getByRole('alert')).toContainText('email is not a valid address');
    await expect(page.getByRole('alert')).toContainText('request id req-');
    expect(stub.count('POST', '/v1/admin/invites')).toBe(1);
  });

  test('text from the api is text: markup in a note renders as characters', async ({ page }) => {
    const errors = watch(page);
    const stub = await setup(page);
    await signIn(page, stub);
    await expect(page.getByText('<b onmouseover="window.__xss=3">Ben</b>')).toBeVisible();
    await expect(page.locator('main b')).toHaveCount(0);
    expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
    expect(errors).toEqual([]);
    expect(stub.invites).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------------------------
test.describe('users, feedback, flags, audit', () => {
  test('users: no email unless asked; filter; grant and revoke access; only the brief columns', async ({
    page,
  }) => {
    const stub = await setup(page);
    await signIn(page, stub);
    await tab(page, 'Users').click();
    await expect(page.getByText('Account 9f8e7d6c')).toBeVisible();
    await expect(page.locator('main')).not.toContainText('anna@example.com');
    expect(stub.seen.filter((s) => s.path === '/v1/admin/users').at(-1)?.search).toBe('');

    await page.getByLabel('Show email addresses').check();
    await expect(page.getByText('anna@example.com')).toBeVisible();
    await expect(page.getByText('Hide My Email')).toBeVisible();
    expect(stub.seen.filter((s) => s.path === '/v1/admin/users').at(-1)?.search).toBe('?emails=1');

    // Exactly the columns of AdminUserBrief, nothing health-derived.
    const labels = await page.locator('li.adm-card').first().locator('dt').allTextContents();
    expect(labels).toEqual(['ID', 'Sign-in', 'Last sync', 'Created']);

    await page.getByLabel('Search').fill('apple');
    await expect(page.locator('li.adm-card')).toHaveCount(1);
    await expect(page.getByText('1 of 2 accounts')).toBeVisible();
    await page.getByLabel('Search').fill('');

    const anna = page.locator('li.adm-card', { hasText: 'anna@example.com' });
    await anna.getByRole('button', { name: 'Grant access' }).click();
    await expect(anna.getByText('Beta access')).toBeVisible();
    expect(stub.count('POST', '/v1/admin/users/9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a/access')).toBe(1);

    const relay = page.locator('li.adm-card', { hasText: 'privaterelay' });
    await relay.getByRole('button', { name: 'Revoke access' }).click();
    await relay.getByRole('button', { name: 'Revoke now' }).click();
    await expect(relay.getByText('No access')).toBeVisible();
    expect(stub.count('DELETE', '/v1/admin/users/0a1b2c3d-4e5f-4061-8283-949a5b6c7d8e/access')).toBe(1);
  });

  test('feedback: shown as text with line breaks, resolve; markup does not run', async ({ page }) => {
    const errors = watch(page);
    const stub = await setup(page);
    await signIn(page, stub);
    await tab(page, 'Feedback').click();
    await expect(page.getByText('Sleep stages are empty for last night.')).toBeVisible();
    const payload = '<img src=x onerror="window.__xss=1"><script>window.__xss=2</script>';
    await expect(page.getByText(payload)).toBeVisible();
    await expect(page.locator('main img, main script')).toHaveCount(0);
    expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
    expect(
      await page
        .locator('.adm-message')
        .first()
        .evaluate((e) => getComputedStyle(e).whiteSpace),
    ).toBe('pre-wrap');
    // No device fields, request ids or diagnostics: the labels are the brief ones.
    const labels = await page.locator('li.adm-card').first().locator('dt').allTextContents();
    expect(labels).toEqual(['Account', 'Sent']);

    const first = page.locator('li.adm-card').first();
    await first.getByRole('button', { name: 'Resolve' }).click();
    await expect(first.getByText('Resolved', { exact: true }).first()).toBeVisible();
    await expect(first.getByRole('button', { name: 'Resolve' })).toHaveCount(0);
    expect(stub.count('POST', '/v1/admin/feedback/42/resolve')).toBe(1);
    expect(errors).toEqual([]);
  });

  test('flags are read-only and show a count, not ids', async ({ page }) => {
    await setup(page).then((stub) => signIn(page, stub));
    await tab(page, 'Flags').click();
    await expect(page.getByText('train.import')).toBeVisible();
    await expect(page.locator('main')).toContainText('Allow-listed accounts');
    await expect(page.locator('main')).not.toContainText('9f8e7d6c-5b4a');
    await expect(page.locator('main button')).toHaveCount(0);
  });

  test('audit lists who did what', async ({ page }) => {
    await setup(page).then((stub) => signIn(page, stub));
    await tab(page, 'Audit').click();
    await expect(page.getByText('POST /v1/admin/invites')).toBeVisible();
    await expect(page.getByText('User 1b2c3d4e').first()).toBeVisible();
    await expect(page.getByText('code: RYFT-7K3M-QX9P')).toBeVisible();
  });
});

// ---------------------------------------------------------------------------------------------
// axe on every screen, both appearances; a screenshot each (phone and desktop projects).
for (const scheme of ['dark', 'light'] as const) {
  test.describe(`accessibility (${scheme})`, () => {
    test.beforeEach(async ({ page }) => {
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });
    });

    async function check(
      page: Page,
      name: string,
      info: { attach: (n: string, o: { body: Buffer; contentType: string }) => Promise<void> },
    ) {
      expect(await serious(page), name).toEqual([]);
      await info.attach(`admin-${name}-${scheme}`, {
        body: await page.screenshot({ fullPage: true }),
        contentType: 'image/png',
      });
    }

    test('sign in, enrol, challenge', async ({ page }, info) => {
      const errors = watch(page);
      const stub = await setup(page);
      await page.goto('/admin');
      await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
      await check(page, 'sign-in', info);
      await page.getByLabel('Email', { exact: true }).fill(EMAIL);
      await page.getByLabel('Password', { exact: true }).fill('nope');
      await page.getByRole('button', { name: 'Continue' }).click();
      await expect(page.getByRole('alert')).toBeVisible();
      expect(await serious(page), 'sign-in with error').toEqual([]);
      await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
      await page.getByRole('button', { name: 'Continue' }).click();
      await expect(page.getByRole('heading', { level: 1, name: 'Set up your authenticator' })).toBeVisible();
      await expect
        .poll(() =>
          page.locator('img.adm-qr').evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth > 0),
        )
        .toBe(true);
      await check(page, 'enrol', info);
      await page.getByLabel('6-digit code').fill(TOTP);
      await page.getByRole('button', { name: 'Verify and continue' }).click();
      await expect(page.getByRole('heading', { level: 1, name: 'Invites' })).toBeVisible();
      await page.getByRole('button', { name: 'Sign out' }).click();
      await password(page);
      await expect(page.getByRole('heading', { level: 1, name: 'Enter your code' })).toBeVisible();
      await check(page, 'challenge', info);
      expect(stub.factors).toHaveLength(1);
      expect(errors).toEqual([]);
    });

    test('invites, users, feedback, flags, audit', async ({ page }, info) => {
      const errors = watch(page);
      const stub = await setup(page);
      await signIn(page, stub);
      await expect(page.getByText('RYFT-H2NP-4WCX')).toBeVisible();
      await check(page, 'invites', info);
      await page.getByLabel('Email (optional)').fill('anna@example.com');
      await page.getByRole('button', { name: 'Create invite' }).click();
      await expect(page.locator('.adm-new')).toBeVisible();
      await check(page, 'invite-created', info);
      await tab(page, 'Users').click();
      await page.getByLabel('Show email addresses').check();
      await expect(page.getByText('anna@example.com')).toBeVisible();
      await check(page, 'users', info);
      await tab(page, 'Feedback').click();
      await expect(page.getByText('Sleep stages are empty')).toBeVisible();
      await check(page, 'feedback', info);
      await tab(page, 'Flags').click();
      await expect(page.getByText('train.import')).toBeVisible();
      await check(page, 'flags', info);
      await tab(page, 'Audit').click();
      await expect(page.getByText('POST /v1/admin/invites')).toBeVisible();
      await check(page, 'audit', info);
      expect(errors).toEqual([]);
    });
  });
}

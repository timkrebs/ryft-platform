import { expect, test } from '@playwright/test';

// The URLs the app links to, the AASA, and how nginx answers around them.
test.describe('routes the app depends on', () => {
  for (const path of [
    '/privacy',
    '/terms',
    '/support',
    '/impressum',
    '/de/impressum',
    '/de/privacy',
    '/de/terms',
  ]) {
    test(`${path} is 200`, async ({ request }) => {
      const res = await request.get(path, { maxRedirects: 0 });
      expect(res.status()).toBe(200);
      expect(res.headers()['content-type']).toContain('text/html');
    });
  }

  test('apple-app-site-association is JSON, no redirect, no extension', async ({ request }) => {
    const res = await request.get('/.well-known/apple-app-site-association', { maxRedirects: 0 });
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toBe('application/json');
    const body = await res.json();
    expect(body.applinks.details[0].appIDs[0]).toMatch(/\.dev\.timkrebs\.ryft$/);
  });

  for (const path of [
    '/day/2026-10-19',
    '/sleep/2026-10-19',
    '/sleep/plan',
    '/insights/behaviors',
    '/insights/hrv',
    '/train/sessions/3f6c1a52-8a9e-4c1b-9d0e-2b7a4f1e9c33',
    '/train/session/3f6c1a52-8a9e-4c1b-9d0e-2b7a4f1e9c33',
    '/train/exercises/3f6c1a52-8a9e-4c1b-9d0e-2b7a4f1e9c33',
    '/train/plan/3f6c1a52-8a9e-4c1b-9d0e-2b7a4f1e9c33',
    '/train/history',
    '/invite/RYFT7Q2K',
    '/auth/callback',
  ]) {
    test(`${path} shows "Open this in RYFT"`, async ({ request }) => {
      const res = await request.get(path, { maxRedirects: 0 });
      expect(res.status()).toBe(200);
      expect(await res.text()).toContain('Open this in RYFT');
      expect(res.headers()['cache-control']).toBe('no-store');
    });
  }

  test('an auth callback with tokens runs no script that reads them', async ({ page }) => {
    await page.goto('/auth/callback?code=abc#access_token=secret&refresh_token=secret');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Open this in RYFT');
    const scripts = await page.locator('script').allTextContents();
    for (const s of scripts) expect(s).not.toMatch(/location|href|hash|search/);
    expect(await page.locator('a[href*="secret"]').count()).toBe(0);
  });

  test('trailing slashes redirect to the canonical path, relatively', async ({ request }) => {
    for (const [from, to] of [
      ['/privacy/', '/privacy'],
      ['/privacy/?x=1', '/privacy?x=1'],
      ['/privacy/2026-10-26/', '/privacy/2026-10-26'],
      ['/licenses/', '/licenses'],
      ['/de/', '/de'],
      ['/de/science/', '/de/science'],
      ['/de/privacy/', '/de/privacy'],
      ['/x//y/', '/x/y'],
    ]) {
      const res = await request.get(from, { maxRedirects: 0 });
      expect(res.status(), from).toBe(301);
      expect(res.headers()['location'], from).toBe(to);
    }
  });

  // The decoded path must never reach Location: "/\evil.com" is another host to a browser, and
  // a tab or CR in the header is worse. These are 404s with the site's 404 page, no redirect.
  test('a trailing-slash path with a backslash, space or control character is a 404, not a redirect', async ({
    request,
  }) => {
    for (const path of [
      '/%5Cevil.com/',
      '/%5Cevil.com',
      '/%09/evil.com/',
      '/%0d/evil.com/',
      '/%0a/evil.com/',
      '/%20/evil.com/',
      '/a/%5Cevil.com/',
      '/_next/static/',
    ]) {
      const res = await request.get(path, { maxRedirects: 0 });
      expect(res.status(), path).toBe(404);
      expect(res.headers()['location'], path).toBeUndefined();
      expect(await res.text(), path).toContain('Page not found');
    }
  });

  test('no redirect points off the site', async ({ request }) => {
    for (const path of ['/%2F%2Fevil.com/', '/a//', '/x//y/', '/de/']) {
      const res = await request.get(path, { maxRedirects: 0 });
      const location = res.headers()['location'];
      if (location === undefined) continue;
      expect(location, path).toMatch(/^\/[A-Za-z0-9]/);
      expect(location, path).not.toMatch(/[\\\s\x00-\x1f\x7f]/);
    }
  });

  test('unknown paths are 404 with the site’s 404 page', async ({ request }) => {
    for (const path of ['/nope', '/de/nope', '/privacy/1999-01-01']) {
      const res = await request.get(path);
      expect(res.status(), path).toBe(404);
      expect(await res.text(), path).toContain('Page not found');
    }
  });

  test('healthz', async ({ request }) => {
    const res = await request.get('/healthz');
    expect(res.status()).toBe(200);
    expect(await res.text()).toBe('ok\n');
  });

  test('security headers and the CSP on pages and errors', async ({ request }) => {
    for (const path of ['/', '/de/privacy', '/nope', '/auth/callback']) {
      const h = (await request.get(path)).headers();
      expect(h['content-security-policy'], path).toMatch(/script-src 'sha256-[^']+'/);
      expect(h['content-security-policy'], path).not.toContain('unsafe-inline');
      expect(h['strict-transport-security'], path).toContain('max-age=31536000');
      expect(h['x-content-type-options'], path).toBe('nosniff');
      expect(h['referrer-policy'], path).toBe('no-referrer');
      expect(h['x-frame-options'], path).toBe('DENY');
      expect(h['set-cookie'], path).toBeUndefined();
    }
  });

  test('hashed assets are immutable, HTML is short-lived', async ({ request, page }) => {
    await page.goto('/');
    const css = await page.locator('link[rel="stylesheet"]').first().getAttribute('href');
    expect((await request.get(css!)).headers()['cache-control']).toContain('immutable');
    expect((await request.get('/')).headers()['cache-control']).toBe('public, max-age=300');
    // The text in force is revalidated; a versioned text is fixed and may be cached.
    for (const path of ['/privacy', '/de/privacy']) {
      expect((await request.get(path)).headers()['cache-control'], path).toBe('no-cache');
    }
    expect((await request.get('/privacy/2026-10-20')).headers()['cache-control']).toBe('public, max-age=300');
  });
});

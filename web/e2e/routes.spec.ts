import { expect, test } from '@playwright/test';
import { sitePath } from '../scripts/check-hosting.mjs';

// The URLs the app links to, the AASA, and how Firebase Hosting answers around them (its emulator:
// npm run e2e:hosting, web D19). What Hosting's docs leave open (header merging, headers on
// redirects, query strings, "//") is settled in e2e/hosting.spec.ts. A Location is compared as a
// path of the site: relative, or absolute on this origin.
test.describe('routes the app depends on', () => {
  for (const path of ['/privacy', '/terms', '/support', '/impressum']) {
    test(`${path} is 200`, async ({ request }) => {
      const res = await request.get(path, { maxRedirects: 0 });
      expect(res.status()).toBe(200);
      expect(res.headers()['content-type']).toContain('text/html');
    });
  }

  // The German pages are gone (web D18); older builds of the app still link to /de/….
  test('the old German URLs redirect to the English page', async ({ request, baseURL }) => {
    for (const [from, to] of [
      ['/de', '/'],
      ['/de/privacy', '/privacy'],
      ['/de/terms', '/terms'],
      ['/de/impressum', '/impressum'],
      ['/de/support', '/support'],
      ['/de/science', '/science'],
      ['/de/beta', '/beta'],
      ['/de/licenses', '/licenses'],
      ['/de/privacy/2026-10-20', '/privacy/2026-10-20'],
      ['/de/terms/2026-10-20', '/terms/2026-10-20'],
      // The trailing slash goes first (to /de…), then the language: two hops.
      ['/de/', '/de'],
      ['/de/privacy/', '/de/privacy'],
    ]) {
      const res = await request.get(from, { maxRedirects: 0 });
      expect.soft(res.status(), from).toBe(301);
      expect.soft(sitePath(baseURL!, res.headers()['location']), from).toBe(to);
    }
    // Followed to the end, every old link lands on an English page.
    for (const from of ['/de/', '/de/privacy/', '/de/impressum']) {
      const res = await request.get(from);
      expect(res.status(), from).toBe(200);
      expect(new URL(res.url()).pathname, from).not.toMatch(/^\/de/);
    }
  });

  // The /de rule builds Location from a capture, like the trailing-slash rule: nothing under /de/
  // may turn into "//host", "/\host" or a header with a control character.
  test('a /de/… path never redirects off the site', async ({ request, baseURL }) => {
    for (const path of [
      '/de/%5Cevil.com',
      '/de/%09/evil.com',
      '/de/%0d/evil.com',
      '/de/%0a/evil.com',
      '/de/%20x',
      '/de//evil.com',
      '/de/%2F%2Fevil.com',
      '/de/a/%5Cb',
    ]) {
      const res = await request.get(path, { maxRedirects: 0 });
      const location = res.headers()['location'];
      if (res.status() === 404) {
        expect(location, path).toBeUndefined();
        continue;
      }
      expect(res.status(), path).toBe(301);
      expect(sitePath(baseURL!, location), path).toMatch(/^\/[A-Za-z0-9]/);
    }
  });

  test('apple-app-site-association is JSON, no redirect, no extension', async ({ request }) => {
    const res = await request.get('/.well-known/apple-app-site-association', { maxRedirects: 0 });
    expect(res.status()).toBe(200);
    // Set by firebase.json's headers rule (the file has no extension); Hosting may add the charset.
    expect(res.headers()['content-type']).toMatch(/^application\/json(?:; ?charset=utf-8)?$/i);
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
    // No script at all (web D20), so nothing on the page can read them.
    expect(await page.locator('script').count()).toBe(0);
    expect(await page.locator('a[href*="secret"]').count()).toBe(0);
  });

  // Query strings and "//" on these redirects: e2e/hosting.spec.ts, (d) and (e).
  test('trailing slashes redirect to the canonical path', async ({ request, baseURL }) => {
    for (const [from, to] of [
      ['/privacy/', '/privacy'],
      ['/science/', '/science'],
      ['/terms/2026-10-20/', '/terms/2026-10-20'],
      ['/privacy/2026-10-26/', '/privacy/2026-10-26'],
      ['/licenses/', '/licenses'],
      ['/de/', '/de'],
      ['/de/science/', '/de/science'],
      ['/de/privacy/', '/de/privacy'],
    ]) {
      const res = await request.get(from, { maxRedirects: 0 });
      expect.soft(res.status(), from).toBe(301);
      expect.soft(sitePath(baseURL!, res.headers()['location']), from).toBe(to);
    }
  });

  // The capture must never reach Location with a backslash, space or control character: "/\evil.com"
  // is another host to a browser, and a tab or CR in the header is worse. These are 404s with the
  // site's 404 page, no redirect.
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
      expect.soft(res.status(), path).toBe(404);
      expect.soft(res.headers()['location'], path).toBeUndefined();
      expect.soft(await res.text(), path).toContain('Page not found');
    }
  });

  test('no redirect points off the site', async ({ request, baseURL }) => {
    for (const path of ['/%2F%2Fevil.com/', '/a//', '/x//y/', '/de/']) {
      const res = await request.get(path, { maxRedirects: 0 });
      const location = res.headers()['location'];
      if (location === undefined) continue;
      expect(sitePath(baseURL!, location), path).toMatch(/^\/[A-Za-z0-9]/);
    }
  });

  test('unknown paths are 404 with the site’s 404 page', async ({ request }) => {
    for (const path of ['/nope', '/privacy/1999-01-01']) {
      const res = await request.get(path);
      expect(res.status(), path).toBe(404);
      expect(await res.text(), path).toContain('Page not found');
    }
  });

  // Header rules match the request path on every status, so a 404 and a rewrite carry them too.
  test('security headers and the CSP on pages and errors', async ({ request }) => {
    for (const path of ['/', '/privacy', '/nope', '/auth/callback']) {
      const h = (await request.get(path)).headers();
      // No page but /admin runs a script (web D20).
      expect(h['content-security-policy'], path).toContain("script-src 'none';");
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
    expect((await request.get('/privacy')).headers()['cache-control']).toBe('no-cache');
    expect((await request.get('/privacy/2026-10-20')).headers()['cache-control']).toBe('public, max-age=300');
  });
});

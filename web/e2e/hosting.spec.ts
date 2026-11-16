import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { cspKind, fetchRaw, sitePath, values } from '../scripts/check-hosting.mjs';

// What Firebase Hosting's docs leave open, settled against the emulator (web D19, design §8):
// (a) one CSP per response, the admin one on /admin only; (b) a headers rule sets the AASA's
// Content-Type; (c) the old German URLs, and what their redirects carry; (d) "//", "/\" and %5C
// never become a Location; (e) query strings on the redirects; (f) one Cache-Control per response.
// Requests go through check-hosting.mjs's fetchRaw: the path exactly as written (a URL parser turns
// "//evil.example/" into another host) and every header as sent, a duplicate counted twice. Each
// test logs what it saw ("observed: …", also an annotation in the report); D19 keeps the results.
// The rules themselves are pinned in tests/hosting.test.ts.

// No browser involved: the desktop project is enough.
test.skip(({ isMobile }) => isMobile, 'HTTP only');

const PAGES = 'public, max-age=300';
const HOUR = 'public, max-age=3600';
const NO_STORE = 'no-store';

type Raw = Awaited<ReturnType<typeof fetchRaw>>;

function observed(text: string) {
  console.log(`observed: ${text}`);
  test.info().annotations.push({ type: 'observed', description: text });
}

const isRedirect = (res: Raw) => res.status >= 300 && res.status < 400;
const origin = (baseURL: string | undefined) => (baseURL ?? 'http://localhost:8080').replace(/\/$/, '');
const shown = (list: string[]) => list.join(' | ') || 'none';

// The path classes of firebase.json: path → its Cache-Control.
const CLASSES: [string, string][] = [
  ['/', PAGES],
  ['/privacy', 'no-cache'],
  ['/privacy/2026-10-26', PAGES],
  ['/admin', NO_STORE],
  ['/admin/', PAGES],
  ['/admin.html', PAGES],
  ['/administrator', PAGES],
  ['/adm', PAGES],
  ['/_next/static/x.css', 'public, max-age=31536000, immutable'],
  ['/icon-192.png', 'public, max-age=86400'],
  ['/.well-known/apple-app-site-association', HOUR],
  ['/invite/abc', NO_STORE],
  ['/de', HOUR],
  ['/de/privacy', HOUR],
  // A 404 has the headers of the path it was asked for (nginx sent 404.html's no-cache).
  ['/x', PAGES],
];

test('(a) and (f): one CSP and one Cache-Control per response', async ({ baseURL }) => {
  const base = origin(baseURL);
  for (const [path, cache] of CLASSES) {
    const res = await fetchRaw(base, path);
    const csp = values(res, 'content-security-policy');
    const cc = values(res, 'cache-control');
    const want = path === '/admin' ? 'admin' : 'marketing';
    const kinds = shown(csp.map((c) => cspKind(c)));
    observed(`${path} → ${res.status}: CSP ${kinds}; Cache-Control ${shown(cc)}`);
    // On a redirect the headers may be missing ((c) records it), never doubled.
    const least = isRedirect(res) ? 0 : 1;
    expect.soft(csp.length, `${path}: CSP headers`).toBeGreaterThanOrEqual(least);
    expect.soft(csp.length, `${path}: CSP headers`).toBeLessThanOrEqual(1);
    expect.soft(cc.length, `${path}: Cache-Control headers`).toBeGreaterThanOrEqual(least);
    expect.soft(cc.length, `${path}: Cache-Control headers`).toBeLessThanOrEqual(1);
    if (csp.length) expect.soft(cspKind(csp[0]), `${path}: the policy`).toBe(want);
    if (cc.length) expect.soft(cc[0], `${path}: Cache-Control`).toBe(cache);
  }
  // Percent-encoded: nginx decoded the path before matching; Hosting may match it as sent.
  const encoded = await fetchRaw(base, '/%61dmin');
  const csp = values(encoded, 'content-security-policy');
  observed(`/%61dmin → ${encoded.status}: CSP ${shown(csp.map((c) => cspKind(c)))}`);
  expect.soft(csp, '/%61dmin: CSP headers').toHaveLength(1);
});

test('(b) the AASA: 200, no redirect, application/json from a headers rule', async ({ baseURL }) => {
  const res = await fetchRaw(origin(baseURL), '/.well-known/apple-app-site-association');
  const type = values(res, 'content-type');
  observed(`AASA → ${res.status}, Content-Type ${shown(type)}`);
  expect.soft(res.status).toBe(200);
  expect.soft(values(res, 'location')).toEqual([]);
  // The file has no extension, so no type of its own: the type is the rule's.
  expect.soft(type).toHaveLength(1);
  expect.soft(type[0]).toMatch(/^application\/json(?:; ?charset=utf-8)?$/i);
  expect.soft(res.body.equals(readFileSync('public/.well-known/apple-app-site-association'))).toBe(true);
});

test('(c) /de → /, /de/ → /de, /de/privacy → /privacy', async ({ baseURL }) => {
  const base = origin(baseURL);
  for (const [from, to] of [
    ['/de', '/'],
    ['/de/', '/de'],
    ['/de/privacy', '/privacy'],
    ['/de/privacy/2026-10-20', '/privacy/2026-10-20'],
  ]) {
    const res = await fetchRaw(base, from);
    const location = values(res, 'location');
    const cache = shown(values(res, 'cache-control'));
    observed(`${from} → ${res.status} ${shown(location)}; Cache-Control ${cache}`);
    expect.soft(res.status, from).toBe(301);
    expect.soft(location, from).toHaveLength(1);
    expect.soft(sitePath(base, location[0]), from).toBe(to);
  }
});

test('(d) /privacy/ → /privacy; //, /\\ and %5C never become a Location', async ({ baseURL }) => {
  const base = origin(baseURL);
  const slash = await fetchRaw(base, '/privacy/');
  expect.soft(slash.status, '/privacy/').toBe(301);
  expect.soft(sitePath(base, values(slash, 'location')[0]), '/privacy/').toBe('/privacy');
  // path → the Locations allowed instead of a 404: what the trailing-slash or /de rule makes of the
  // path as sent, or with "//" merged into "/" (if the emulator merges before the rules run).
  const CASES: [string, string[]][] = [
    ['/x//', ['/x/', '/x']],
    ['//evil.example/', ['/evil.example']],
    ['/de//evil.example', ['/evil.example']],
    ['/%2F%2Fevil.example/', ['/evil.example']],
    ['/%5Cevil.example/', []],
    ['/de/%5Cevil.example', []],
    ['/a/%5Cevil.example/', []],
  ];
  for (const [path, allowed] of CASES) {
    const res = await fetchRaw(base, path);
    const location = values(res, 'location');
    observed(`${path} → ${res.status} ${shown(location)}`);
    for (const l of location) {
      expect.soft(sitePath(base, l), `${path}: Location ${l} leaves the site`).not.toBeNull();
    }
    if (res.status === 404) {
      expect.soft(location, path).toEqual([]);
      expect.soft(res.body.toString('utf8'), path).toContain('Page not found');
    } else {
      expect.soft(res.status, `${path}: a 404 or a redirect on the site`).toBe(301);
      expect.soft(allowed, `${path}: Location`).toContain(sitePath(base, location[0]));
    }
  }
});

test('(e) query strings on the redirects', async ({ baseURL }) => {
  const base = origin(baseURL);
  for (const [from, to] of [
    ['/de?x=1', '/'],
    ['/de/privacy?x=1&y=2', '/privacy'],
    ['/de/?x=1', '/de'],
    ['/privacy/?x=1', '/privacy'],
    ['/admin.html?x=1', '/admin'],
  ]) {
    const res = await fetchRaw(base, from);
    const location = sitePath(base, values(res, 'location')[0]);
    const query = from.slice(from.indexOf('?'));
    const kept = location === `${to}${query}` ? 'kept' : location === to ? 'dropped' : 'changed';
    observed(`${from} → ${res.status} ${location}: the query ${kept}`);
    expect.soft(res.status, from).toBe(301);
    expect.soft([to, `${to}${query}`], from).toContain(location);
  }
});

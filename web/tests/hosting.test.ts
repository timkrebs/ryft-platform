import { describe, expect, it } from 'vitest';
import config from '../config/admin.json';
import {
  cspKind,
  parseArgs,
  redirectWant,
  responseProblems,
  shownVersion,
  sitePath,
  slashWant,
  values,
} from '../scripts/check-hosting.mjs';
import { adminPolicy, policy } from '../scripts/lib/csp.mjs';
import { CACHE, SHARED_HEADERS, allExcept, firebaseJson, jsRegExp } from '../scripts/lib/hosting.mjs';

// firebase.json for Firebase Hosting (web D19), as scripts/firebase-json.mjs generates it, rule by
// rule, with fixture hashes (tests/output.test.ts checks the built file). Hosting applies every
// `headers` rule whose pattern matches the request path, and does not document how it combines two
// that set the same header. So the CSP rules must be disjoint (exactly one matches any path), and
// Cache-Control is read as "the last matching rule wins". What the emulator does with all of it is
// settled in e2e/hosting.spec.ts.

type Rule = { source?: string; regex?: string; destination: string; type?: number };
type Header = { key: string; value: string };
type HeaderRule = { regex: string; source?: string; glob?: string; headers: Header[] };
type Hosting = {
  [key: string]: unknown;
  public: string;
  cleanUrls: boolean;
  ignore: string[];
  redirects: Rule[];
  rewrites: Rule[];
  headers: HeaderRule[];
};
type FirebaseJson = { hosting: Hosting; emulators: unknown };

// The marketing pages run no script (web D20), so they have no hashes: script-src 'none'.
const HASHES: string[] = [];
const ADMIN_HASHES = ["'sha256-YWRtaW4='"];
const MARKETING = policy(HASHES);
const ADMIN = adminPolicy(ADMIN_HASHES, config);
const input = { hashes: HASHES, adminHashes: ADMIN_HASHES, config };
const fb = firebaseJson(input) as unknown as FirebaseJson;
const hosting = fb.hosting;
const AASA = '/.well-known/apple-app-site-association';

const rules = hosting.headers.map((r) => ({ ...r, re: jsRegExp(r.regex) }));
/** The header rules that set `key`, in file order. */
const sets = (key: string) => hosting.headers.filter((r) => r.headers.some((h) => h.key === key));

/** Every value the rules matching `path` set for `key`, in file order. */
function sent(path: string, key: string): string[] {
  const out: string[] = [];
  for (const r of rules) {
    if (!r.re.test(path)) continue;
    for (const h of r.headers) if (h.key === key) out.push(h.value);
  }
  return out;
}

/** Every string over `alphabet` of length 0 to `max`. */
function* strings(alphabet: string[], max: number, prefix = ''): Generator<string> {
  yield prefix;
  if (prefix.length === max) return;
  for (const c of alphabet) yield* strings(alphabet, max, prefix + c);
}

// What RE2 cannot do, and JavaScript's spelling of a named group (RE2 and the docs: (?P<name>…)).
const NOT_RE2: [RegExp, string][] = [
  [/\(\?<?[=!]/, 'lookaround'],
  [/\\[1-9]|\\k</, 'a backreference'],
  [/\(\?>/, 'an atomic group'],
  [/[*+?}]\+/, 'a possessive quantifier'],
  [/\(\?\(/, 'a conditional'],
  [/\(\?<[A-Za-z]/, 'a named group not written (?P<name>…)'],
];

// The paths the e2e tests ask for, and the edges of "every path but /admin".
const PATHS = [
  '/',
  '/privacy',
  '/privacy/2026-10-26',
  '/admin',
  '/admin/',
  '/admin.html',
  '/administrator',
  '/adm',
  '/admi',
  '/a',
  '/Admin',
  '/xadmin',
  '/admin/x',
  '/admin\n',
  '//admin',
  '/%61dmin',
  '/_next/static/x.css',
  '/icon-192.png',
  AASA,
  '/invite/abc',
  '/de',
  '/de/privacy',
  '/x',
];

describe('firebase.json (scripts/lib/hosting.mjs)', () => {
  it('has the decided keys and nothing else', () => {
    expect(Object.keys(fb).sort()).toEqual(['emulators', 'hosting']);
    expect(Object.keys(hosting).sort()).toEqual([
      'cleanUrls',
      'headers',
      'ignore',
      'public',
      'redirects',
      'rewrites',
    ]);
    expect(hosting.public).toBe('out');
    expect(hosting.cleanUrls).toBe(true);
    // Unset: Hosting redirects no trailing slash itself; #156's rule does, for safe paths only.
    expect(hosting).not.toHaveProperty('trailingSlash');
    // Only the precompressed copies: firebase init's "**/.*" would drop .well-known/ and the AASA.
    expect(hosting.ignore).toEqual(['**/*.gz']);
    expect(fb.emulators).toEqual({ hosting: { host: 'localhost', port: 8080 }, ui: { enabled: false } });
  });

  it('redirects #156, /de and /de/<path>, and rewrites the app links to open.html', () => {
    expect(hosting.redirects).toEqual([
      { regex: '^/(?P<p>[A-Za-z0-9][A-Za-z0-9/._-]*)/$', destination: '/:p', type: 301 },
      { source: '/de', destination: '/', type: 301 },
      { regex: '^/de/(?P<p>[A-Za-z0-9][A-Za-z0-9/._-]*)$', destination: '/:p', type: 301 },
    ]);
    expect(hosting.rewrites).toEqual([
      { regex: '^/(day|sleep|insights|journal|train|auth|invite)(/.*)?$', destination: '/open.html' },
    ]);
  });

  it('writes every pattern as an anchored regex in the RE2 subset, and headers by regex only', () => {
    const all = [...hosting.redirects, ...hosting.rewrites, ...hosting.headers];
    const patterns = all.flatMap((r) => (r.regex === undefined ? [] : [r.regex]));
    expect(patterns).toHaveLength(2 + 1 + hosting.headers.length);
    for (const p of patterns) {
      for (const [bad, what] of NOT_RE2) expect.soft(bad.test(p), `${p}: ${what}`).toBe(false);
      expect.soft(p, `${p}: anchored`).toMatch(/^\^.*\$$/);
      expect.soft(() => jsRegExp(p), p).not.toThrow();
    }
    for (const r of hosting.headers) {
      expect.soft(r.source, r.regex).toBeUndefined();
      expect.soft(r.glob, r.regex).toBeUndefined();
    }
    // A destination's :name is a named group of its own pattern.
    for (const r of [...hosting.redirects, ...hosting.rewrites]) {
      for (const [, name] of r.destination.matchAll(/:(\w+)/g)) {
        expect.soft(r.regex ?? '', r.destination).toContain(`(?P<${name}>`);
      }
    }
  });

  it('sends exactly one CSP on every path: the admin policy on /admin only', () => {
    const cspRules = sets('Content-Security-Policy').map((r) => r.regex);
    expect(cspRules).toEqual([`^/${allExcept('admin')}$`, '^/admin$']);
    for (const path of PATHS) {
      const want = path === '/admin' ? ADMIN : MARKETING;
      expect.soft(sent(path, 'Content-Security-Policy'), JSON.stringify(path)).toEqual([want]);
    }
  });

  it('leaves exactly /admin out of the marketing rule, for every path of up to seven characters', () => {
    const [marketing, admin] = sets('Content-Security-Policy').map((r) => jsRegExp(r.regex));
    const wrong: string[] = [];
    for (const rest of strings(['a', 'd', 'm', 'i', 'n', 'x', '/', '\n'], 6)) {
      const path = `/${rest}`;
      const isAdmin = path === '/admin';
      const right = marketing.test(path) !== isAdmin && admin.test(path) === isAdmin;
      if (!right) wrong.push(JSON.stringify(path));
    }
    expect(wrong).toEqual([]);
  });

  it('caches as nginx did', () => {
    expect(CACHE).toEqual({
      pages: 'public, max-age=300',
      hashed: 'public, max-age=31536000, immutable',
      files: 'public, max-age=86400',
      german: 'public, max-age=3600',
      aasa: 'public, max-age=3600',
      inForce: 'no-cache',
      never: 'no-store',
    });
  });

  it('gives every path a Cache-Control, and the last matching rule is its class', () => {
    const table: [string, string][] = [
      ['/', CACHE.pages],
      ['/privacy', CACHE.inForce],
      ['/privacy/2026-10-26', CACHE.pages],
      ['/privacy.html', CACHE.pages],
      ['/admin', CACHE.never],
      ['/admin/', CACHE.pages],
      ['/admin.html', CACHE.pages],
      ['/administrator', CACHE.pages],
      ['/adm', CACHE.pages],
      ['/_next/static/x.css', CACHE.hashed],
      ['/_next/static/media/a.woff2', CACHE.hashed],
      // nginx's extension location ranked before /_next/static/: kept.
      ['/_next/static/media/a.svg', CACHE.files],
      ['/icon-192.png', CACHE.files],
      ['/site.webmanifest', CACHE.files],
      ['/sitemap.xml', CACHE.files],
      ['/robots.txt', CACHE.files],
      ['/screenshots/x.webp', CACHE.files],
      [AASA, CACHE.aasa],
      ['/invite/abc', CACHE.never],
      ['/auth/callback', CACHE.never],
      ['/day', CACHE.never],
      ['/day/x.png', CACHE.never],
      ['/dayx', CACHE.pages],
      ['/open', CACHE.pages],
      ['/de', CACHE.german],
      ['/de/privacy', CACHE.german],
      ['/de/privacy/2026-10-20', CACHE.german],
      ['/de/x.png', CACHE.german],
      ['/de/', CACHE.pages],
      ['/de/%5Cevil.example', CACHE.pages],
      ['/deutsch', CACHE.pages],
      ['/x', CACHE.pages],
      ['/privacy.html.gz', CACHE.pages],
    ];
    for (const [path, want] of table) {
      const all = sent(path, 'Cache-Control');
      expect.soft(all.length, path).toBeGreaterThan(0);
      expect.soft(all.at(-1), path).toBe(want);
    }
  });

  it('sets every other header from one rule: the shared set on every path, the rest once', () => {
    for (const path of PATHS) {
      for (const { key, value } of SHARED_HEADERS) {
        expect.soft(sent(path, key), `${JSON.stringify(path)} ${key}`).toEqual([value]);
      }
      const robots = path === '/admin' ? ['noindex, nofollow'] : [];
      expect.soft(sent(path, 'X-Robots-Tag'), JSON.stringify(path)).toEqual(robots);
      const type = path === AASA ? ['application/json'] : [];
      expect.soft(sent(path, 'Content-Type'), JSON.stringify(path)).toEqual(type);
    }
    for (const { key } of SHARED_HEADERS) expect.soft(sets(key), key).toHaveLength(1);
    expect(sets('X-Robots-Tag')).toHaveLength(1);
    expect(sets('Content-Type')).toHaveLength(1);
    for (const r of hosting.headers) {
      const keys = r.headers.map((h) => h.key);
      expect.soft(new Set(keys).size, r.regex).toBe(keys.length);
    }
  });

  it('puts /admin last, with its own policy, no-store and noindex', () => {
    expect(hosting.headers.at(-1)).toEqual({
      regex: '^/admin$',
      headers: [
        { key: 'Content-Security-Policy', value: ADMIN },
        { key: 'Cache-Control', value: 'no-store' },
        { key: 'X-Robots-Tag', value: 'noindex, nofollow' },
      ],
    });
  });

  // The shared set, value for value: the one the nginx image sent until web D25, now pinned here.
  it('carries the shared security headers, value for value', () => {
    const want: Header[] = [
      { key: 'X-Content-Type-Options', value: 'nosniff' },
      { key: 'Referrer-Policy', value: 'no-referrer' },
      { key: 'X-Frame-Options', value: 'DENY' },
      { key: 'Strict-Transport-Security', value: 'max-age=31536000; includeSubDomains' },
      { key: 'Cross-Origin-Opener-Policy', value: 'same-origin' },
      { key: 'Cross-Origin-Resource-Policy', value: 'same-origin' },
      {
        key: 'Permissions-Policy',
        value:
          'accelerometer=(), camera=(), geolocation=(), gyroscope=(), microphone=(), payment=(), usb=(), browsing-topics=()',
      },
    ];
    expect(SHARED_HEADERS).toEqual(want);
    // Share and clipboard-write stay allowed.
    expect(SHARED_HEADERS.map((h) => h.value).join()).not.toMatch(/web-share|clipboard-write/);
  });
});

// An open redirect (#156): a capture that becomes Location must never start with "//" or "/\"
// (another host to a browser) or carry a backslash, space or control character.
describe('the redirects keep Location on the site', () => {
  const [slash, de, deCapture] = hosting.redirects;
  const redirects = hosting.redirects.map((r) => ({ ...r, re: r.regex ? jsRegExp(r.regex) : null }));

  /** Hosting's redirects in order, the first match wins: the Location a path gets, or null. */
  function location(path: string): string | null {
    for (const r of redirects) {
      if (r.re === null) {
        if (path === r.source) return r.destination;
        continue;
      }
      const m = r.re.exec(path);
      if (m) return r.destination.replace(/:(\w+)/g, (_, name: string) => m.groups?.[name] ?? '');
    }
    return null;
  }

  /** The hops from `path` until no rule answers (at most five). */
  function follow(path: string): string[] {
    const hops = [path];
    for (let i = 0; i < 5; i++) {
      const next = location(hops[hops.length - 1]);
      if (next === null) break;
      hops.push(next);
    }
    return hops;
  }

  it('the trailing-slash capture takes no host, backslash, space or control character', () => {
    const re = jsRegExp(slash.regex);
    for (const path of [
      '/\\evil.com/',
      '/\t/evil.com/',
      '/\r/evil.com/',
      '/\n/evil.com/',
      '/ /evil.com/',
      '/a/\\evil.com/',
      '//evil.com/',
      '/%5Cevil.com/',
      '/%2F%2Fevil.com/',
      '/%09/evil.com/',
      '/./',
      '/_next/static/',
      '/',
    ]) {
      expect.soft(re.test(path), JSON.stringify(path)).toBe(false);
    }
    for (const [from, to] of [
      ['/privacy/', '/privacy'],
      ['/privacy/2026-10-26/', '/privacy/2026-10-26'],
      ['/licenses/', '/licenses'],
      ['/admin/', '/admin'],
      ['/de/', '/de'],
      ['/de/science/', '/de/science'],
      // The path as sent: "//" stays in the capture, and Location stays on the site.
      ['/a//', '/a/'],
      ['/x//', '/x/'],
      ['/x//y/', '/x//y'],
    ]) {
      expect.soft(location(from), from).toBe(to);
    }
  });

  it('sends bare /de to / by an exact rule, and /de/<path> on through the safe capture only', () => {
    expect(de).toEqual({ source: '/de', destination: '/', type: 301 });
    const re = jsRegExp(deCapture.regex);
    for (const path of [
      '/de/\\evil.com',
      '/de/\t/evil.com',
      '/de/\r/evil.com',
      '/de/\n/evil.com',
      '/de/ x',
      '/de//evil.com',
      '/de/a/\\b',
      '/de/.hidden',
      '/de/%5Cevil.example',
      '/de/%2F%2Fevil.com',
      '/deutsch',
      '/de',
      '/de/',
    ]) {
      expect.soft(re.test(path), JSON.stringify(path)).toBe(false);
    }
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
      // "/de//evil.com" with its slashes merged: a path of this site.
      ['/de/evil.com', '/evil.com'],
    ]) {
      expect.soft(location(from), from).toBe(to);
    }
    expect(location('/de/%5Cevil.example')).toBeNull();
    // A trailing slash first, then the language: two hops, as with nginx.
    expect(follow('/de/')).toEqual(['/de/', '/de', '/']);
    expect(follow('/de/privacy/')).toEqual(['/de/privacy/', '/de/privacy', '/privacy']);
  });

  it('gives every path "/" or "/" + a letter or digit as Location, or no redirect', () => {
    const wrong: string[] = [];
    for (const rest of strings(['/', '\\', '.', 'd', 'e', 'x', '%', ' '], 6)) {
      const to = location(`/${rest}`);
      if (to !== null && !/^\/(?:[A-Za-z0-9][A-Za-z0-9/._-]*)?$/.test(to)) wrong.push(`/${rest} → ${to}`);
    }
    expect(wrong).toEqual([]);
  });
});

describe('check-hosting.mjs', () => {
  const BASE = 'http://localhost:8080';
  type Raw = Parameters<typeof responseProblems>[1];
  type Want = Parameters<typeof responseProblems>[2];
  const check = (res: Raw, want: Want) => responseProblems(BASE, res, want);

  /** A response with every header the site sends, once, then `extra`. */
  function response(status: number, extra: [string, string][] = [], csp = MARKETING): Raw {
    const headers: [string, string][] = [
      ['content-security-policy', csp],
      ...SHARED_HEADERS.map((h): [string, string] => [h.key.toLowerCase(), h.value]),
      ['cache-control', CACHE.pages],
      ...extra,
    ];
    return { status, headers, body: Buffer.from(status === 404 ? 'Page not found' : '<html>') };
  }

  /** Google's front end's 307 (web D21): the Location, if any, and X-Frame-Options: SAMEORIGIN only. */
  function front(location?: string): Raw {
    const headers: [string, string][] = [['x-frame-options', 'SAMEORIGIN']];
    if (location !== undefined) headers.unshift(['location', location]);
    return { status: 307, headers, body: Buffer.alloc(0) };
  }
  // run()'s two "//" probes, which the front end answers live.
  const EVIL: Want = {
    status: [301, 307, 404],
    location: ['/evil.example', '/evil.example/'],
    csp: 'marketing',
    frontEnd: true,
  };
  const X: Want = { status: [301, 307, 404], location: ['/x/', '/x'], csp: 'marketing', frontEnd: true };

  it('reads its flags', () => {
    expect(parseArgs(['--base', 'https://ryftlab.de/'])).toEqual({
      base: 'https://ryftlab.de',
      www: '',
      policyVersion: '',
      appleCdn: false,
      emulator: false,
      help: false,
    });
    const all = parseArgs([
      '--base=http://localhost:8080',
      '--www',
      'https://www.ryftlab.de',
      '--policy-version',
      '2026-10-26',
      '--apple-cdn',
    ]);
    expect(all).toEqual({
      base: 'http://localhost:8080',
      www: 'https://www.ryftlab.de',
      policyVersion: '2026-10-26',
      appleCdn: true,
      emulator: false,
      help: false,
    });
    for (const bad of [
      [],
      ['--www', 'https://www.ryftlab.de'],
      ['--base'],
      ['--base', 'https://ryftlab.de/privacy'],
      ['--base', 'ryftlab.de'],
      ['--base', 'https://ryftlab.de', '--policy-version', '2026-1-1'],
      ['--base', 'https://ryftlab.de', '--live'],
    ]) {
      expect(() => parseArgs(bad), bad.join(' ')).toThrow();
    }
  });

  it('reads --emulator, and expects production without it (web D21)', () => {
    expect(parseArgs(['--base', 'https://ryftlab.de']).emulator).toBe(false);
    expect(parseArgs(['--base', 'http://localhost:8080', '--emulator']).emulator).toBe(true);
    expect(parseArgs(['--emulator', '--base=http://localhost:8080']).emulator).toBe(true);
    expect(() => parseArgs(['--base', 'http://localhost:8080', '--emulator=no'])).toThrow();
  });

  it('accepts a Location on this site only', () => {
    expect(sitePath(BASE, '/privacy')).toBe('/privacy');
    expect(sitePath(BASE, '/')).toBe('/');
    expect(sitePath(BASE, `${BASE}/privacy?x=1`)).toBe('/privacy?x=1');
    for (const bad of [
      '//evil.example',
      '/\\evil.example',
      '/a\\b',
      '/a b',
      '/a\tb',
      '/a\r\nSet-Cookie: x',
      'https://evil.example/',
      `${BASE}//evil.example`,
      'privacy',
      undefined,
    ]) {
      expect(sitePath(BASE, bad), JSON.stringify(bad)).toBeNull();
    }
  });

  it('tells the two policies apart', () => {
    expect(MARKETING).toContain("script-src 'none';");
    expect(cspKind(MARKETING)).toBe('marketing');
    expect(cspKind(ADMIN)).toBe('admin');
    // The marketing pages run no script (web D20): a policy that lets one run is not theirs.
    expect(cspKind(policy(["'sha256-bWFya2V0aW5nLTE='"]))).toBe('other');
    expect(cspKind(adminPolicy([], config))).toBe('other');
    expect(cspKind("default-src 'self'; script-src 'unsafe-inline'")).toBe('other');
    expect(cspKind(`${MARKETING}; trusted-types 'none'`)).toBe('other');
  });

  it('counts a header sent twice as two', () => {
    const res = response(200, [['content-security-policy', MARKETING]]);
    expect(values(res, 'content-security-policy')).toEqual([MARKETING, MARKETING]);
    expect(check(res, { status: [200], csp: 'marketing' })).toEqual(['2 × Content-Security-Policy']);
  });

  it('passes a page, a redirect without headers and a 404, and names what is wrong', () => {
    expect(check(response(200), { status: [200], csp: 'marketing', cache: CACHE.pages })).toEqual([]);
    expect(check(response(404), { status: [404], csp: 'marketing' })).toEqual([]);
    const bare: Raw = { status: 301, headers: [['location', '/privacy']], body: Buffer.alloc(0) };
    expect(check(bare, { status: [301], location: ['/privacy'], csp: 'marketing' })).toEqual([]);

    const away: Raw = { status: 301, headers: [['location', '//evil.example/']], body: Buffer.alloc(0) };
    expect(check(away, { status: [301], location: ['/evil.example'], csp: 'marketing' })).toContain(
      'Location "//evil.example/" leaves the site',
    );
    expect(check(response(200, [], ADMIN), { status: [200], csp: 'marketing' })).toEqual([
      'the admin CSP, not the marketing one',
    ]);
    expect(check(response(200), { status: [200], csp: 'admin', robots: true })).toEqual([
      'the marketing CSP, not the admin one',
      'X-Robots-Tag: []',
    ]);
    expect(check(response(404, [['location', '/x']]), { status: [404], csp: 'marketing' })).toEqual([
      'a Location without a redirect',
    ]);
    expect(check(response(200), { status: [200], csp: 'marketing', cache: CACHE.never })).toEqual([
      'Cache-Control: public, max-age=300, not no-store',
    ]);
    const twice = response(200, [['strict-transport-security', 'max-age=1']]);
    expect(check(twice, { status: [200], csp: 'marketing' })).toEqual(['2 × Strict-Transport-Security']);
    const page = { ...response(404), body: Buffer.from('<h1>404</h1>') };
    expect(check(page, { status: [404], csp: 'marketing' })).toEqual(["not the site's 404 page"]);
  });

  it('takes the front end\'s 307 on "//" in production only (web D21)', () => {
    expect(slashWant('//evil.example/')).toEqual(EVIL);
    expect(slashWant('/x//')).toEqual(X);
    // --emulator: what it expected before web D21, exactly.
    expect(slashWant('//evil.example/', { emulator: true })).toEqual({
      status: [301, 404],
      location: ['/evil.example'],
      csp: 'marketing',
    });
    expect(slashWant('/x//', { emulator: true })).toEqual({
      status: [301, 404],
      location: ['/x/', '/x'],
      csp: 'marketing',
    });
    expect(check(front('/evil.example/'), slashWant('//evil.example/', { emulator: true }))).toEqual([
      'status 307',
      'Location /evil.example/, not /evil.example',
      'X-Frame-Options: SAMEORIGIN',
    ]);
  });

  it("passes the front end's 307 on the two // probes (web D21)", () => {
    expect(check(front('/evil.example/'), EVIL)).toEqual([]);
    expect(check(front('/x/'), X)).toEqual([]);
  });

  it("fails the front end's 307 unless its one Location is a listed path of this site", () => {
    for (const bad of ['//evil.example', '//evil.example/', '/\\evil.example', 'https://evil.example/']) {
      expect(check(front(bad), EVIL), bad).toContain(`Location ${JSON.stringify(bad)} leaves the site`);
    }
    expect(check(front(), EVIL)).toEqual(['0 Location headers']);
    expect(check(front('/elsewhere'), EVIL)).toEqual([
      'Location /elsewhere, not /evil.example or /evil.example/',
    ]);
    const twice = front('/evil.example/');
    twice.headers.push(['location', '/evil.example']);
    expect(check(twice, EVIL)).toEqual(['2 Location headers']);
  });

  it('fails a 307 the probe does not allow, and frontEnd relaxes nothing but a 307', () => {
    const plain: Want = { status: EVIL.status, location: EVIL.location, csp: 'marketing' };
    expect(check(front('/evil.example/'), plain)).toEqual(['X-Frame-Options: SAMEORIGIN']);
    expect(check(front('/evil.example/'), { ...EVIL, status: [301, 404] })).toEqual(['status 307']);
    const moved = { ...front('/evil.example'), status: 301 };
    expect(check(moved, EVIL)).toEqual(['X-Frame-Options: SAMEORIGIN']);
    // A header sent twice still fails, front end or not.
    const doubled = front('/evil.example/');
    doubled.headers.push(['x-frame-options', 'DENY']);
    expect(check(doubled, EVIL)).toEqual(['2 × X-Frame-Options']);
  });

  it("wants /admin's headers on the admin redirects live (web D21)", () => {
    const to: [string, string] = ['location', '/admin'];
    const robots: [string, string] = ['x-robots-tag', 'noindex, nofollow'];
    const live = redirectWant('/admin');
    expect(live).toEqual({ status: [301], location: ['/admin'], csp: 'admin', robots: true });
    expect(check(response(301, [to, robots], ADMIN), live)).toEqual([]);
    expect(check(response(301, [to, robots]), live)).toEqual(['the marketing CSP, not the admin one']);
    expect(check(response(301, [to], ADMIN), live)).toEqual(['X-Robots-Tag: []']);
    // Every other redirect: the marketing CSP, in both modes.
    for (const emulator of [false, true]) {
      const want = redirectWant('/privacy', { emulator });
      expect(want).toEqual({ status: [301], location: ['/privacy'], csp: 'marketing' });
    }
  });

  it("accepts the emulator's admin redirects under --emulator", () => {
    const to: [string, string] = ['location', '/admin'];
    const emulator = redirectWant('/admin', { emulator: true });
    expect(emulator).toEqual({ status: [301], location: ['/admin'], csp: 'marketing' });
    // /admin.html's 301 (cleanUrls): the marketing headers, 8 of 8.
    expect(check(response(301, [to]), emulator)).toEqual([]);
    // /admin/'s 301 (a redirects rule): no headers at all.
    const bare: Raw = { status: 301, headers: [to], body: Buffer.alloc(0) };
    expect(check(bare, emulator)).toEqual([]);
  });

  it('reads the version /privacy shows', () => {
    const html = '<p><span>Version 2026-10-26<!-- --> (the app asks for your consent)</span>';
    expect(shownVersion(html, 'Version {version}')).toBe('2026-10-26');
    expect(shownVersion('<span>Version 2026-10-2</span>', 'Version {version}')).toBeNull();
    // The template from messages/en.json (legal.version).
    expect(shownVersion(html)).toBe('2026-10-26');
  });
});

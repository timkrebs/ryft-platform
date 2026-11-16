#!/usr/bin/env node
// The site as Firebase Hosting serves it (web D19): runbook step 10's assertions (docs/OPERATIONS.md
// → GCP → 10, design §8). Run against the Hosting emulator in CI (npm run e2e:hosting), against
// https://ryftlab.de after every deploy (deploy.yml, `site`) and by hand at step 10.
//
//   node web/scripts/check-hosting.mjs --base <origin> [--www <origin>]
//                                      [--policy-version YYYY-MM-DD] [--apple-cdn]
//
//   --base            the site: http://localhost:8080, https://ryftlab.de
//   --www             another host whose /privacy must redirect to <base>/privacy
//   --policy-version  the privacy version /privacy must show (its "Version …" line)
//   --apple-cdn       Apple's CDN must hold the AASA <base> serves (a public https base only)
//
// One PASS or FAIL line per check, exit 1 if any failed, 2 on a usage error. Node built-ins only:
// every request goes out through node:http(s) with the path exactly as written (no URL parser, so
// "//" and "%5C" reach the server as they are), and headers are counted from res.rawHeaders, so a
// header sent twice counts twice. On a 3xx the security headers may be missing (whether Hosting
// adds them there is one of D19's points), but never doubled; the PASS line says what came.
import { existsSync, readFileSync } from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { isDeepStrictEqual } from 'node:util';
import { CACHE, SHARED_HEADERS } from './lib/hosting.mjs';

/** @typedef {{ status: number, headers: [string, string][], body: Buffer, error?: string }} RawResponse */
/**
 * @typedef {object} Want
 * @property {number[]} status the statuses that pass
 * @property {string[]} [location] on a 3xx: the paths of base Location may name
 * @property {'marketing' | 'admin'} csp which policy the response carries
 * @property {string} [cache] the one Cache-Control value (not checked on a 3xx)
 * @property {RegExp} [type] the one Content-Type
 * @property {boolean} [robots] X-Robots-Tag "noindex, nofollow" (else none)
 * @property {Buffer} [body] the exact body
 */

const WEB = fileURLToPath(new URL('..', import.meta.url));
const AASA = '/.well-known/apple-app-site-association';
const HTML = /^text\/html(?:;\s*charset=utf-8)?$/i;
const JSON_TYPE = /^application\/json(?:;\s*charset=utf-8)?$/i;
const APPLE_CDN = 'https://app-site-association.cdn-apple.com';
const USAGE =
  'usage: node web/scripts/check-hosting.mjs --base <origin> [--www <origin>] [--policy-version YYYY-MM-DD] [--apple-cdn]';

/** The command line, or an Error. Origins lose one trailing slash. */
export function parseArgs(argv) {
  const opts = { base: '', www: '', policyVersion: '', appleCdn: false, help: false };
  const names = { '--base': 'base', '--www': 'www', '--policy-version': 'policyVersion' };
  for (let i = 0; i < argv.length; i++) {
    const [flag, inline] = argv[i].split(/=(.*)/s);
    let value = inline;
    if (flag === '--help' || flag === '-h') opts.help = true;
    else if (flag === '--apple-cdn' && value === undefined) opts.appleCdn = true;
    else if (Object.hasOwn(names, flag)) {
      if (value === undefined) value = argv[++i];
      if (value === undefined || value.startsWith('--')) throw new Error(`${flag} needs a value`);
      opts[names[flag]] = value;
    } else throw new Error(`unknown argument ${JSON.stringify(argv[i])}`);
  }
  if (opts.help) return opts;
  for (const key of ['base', 'www']) {
    opts[key] = opts[key].replace(/\/$/, '');
    if (opts[key] && !/^https?:\/\/[A-Za-z0-9.-]+(?::\d+)?$/.test(opts[key])) {
      throw new Error(`--${key} must be an origin (scheme://host[:port]): ${JSON.stringify(opts[key])}`);
    }
  }
  if (!opts.base) throw new Error('--base <origin> is required');
  if (opts.policyVersion && !/^\d{4}-\d{2}-\d{2}$/.test(opts.policyVersion)) {
    throw new Error(`--policy-version must be YYYY-MM-DD, not ${JSON.stringify(opts.policyVersion)}`);
  }
  return opts;
}

/**
 * One request, `path` exactly as written, no redirect followed, no compression asked for.
 * @param {string} origin
 * @param {string} path
 * @returns {Promise<RawResponse>}
 */
export function fetchRaw(origin, path, timeout = 20000) {
  const url = new URL(origin); // the origin only: the path never goes through a parser
  const client = url.protocol === 'https:' ? https : http;
  return new Promise((done, reject) => {
    const req = client.request(
      {
        protocol: url.protocol,
        hostname: url.hostname.replace(/^\[|\]$/g, ''),
        port: url.port || undefined,
        method: 'GET',
        path,
        agent: false,
        timeout,
        headers: { 'accept-encoding': 'identity', 'user-agent': 'ryft-check-hosting' },
      },
      (res) => {
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('error', reject);
        res.on('end', () => {
          const headers = [];
          for (let i = 0; i < res.rawHeaders.length; i += 2) {
            headers.push([res.rawHeaders[i].toLowerCase(), res.rawHeaders[i + 1]]);
          }
          done({ status: res.statusCode ?? 0, headers, body: Buffer.concat(chunks) });
        });
      },
    );
    req.on('timeout', () => req.destroy(new Error(`no answer within ${timeout} ms`)));
    req.on('error', reject);
    req.end();
  });
}

/**
 * Every value of header `name` (lowercase), one entry per time it was sent.
 * @param {RawResponse} res
 * @param {string} name
 * @returns {string[]}
 */
export function values(res, name) {
  return res.headers.filter(([n]) => n === name).map(([, v]) => v);
}

/**
 * The path of `base` a Location names, or null when it leaves the site or could be read as another
 * host: "//host", "/\host", a backslash, space or control character anywhere, a relative
 * reference, or another origin.
 * @param {string} base
 * @param {string | undefined} location
 * @returns {string | null}
 */
export function sitePath(base, location) {
  if (location === undefined || /[\\\s\p{Cc}]/u.test(location)) return null;
  const path = location.startsWith(`${base}/`) ? location.slice(base.length) : location;
  if (!path.startsWith('/') || path.startsWith('//')) return null;
  return path;
}

/**
 * Which of the site's two policies a CSP value is: 'marketing' (scripts by hash, connect-src
 * 'none'), 'admin' (one hash, the Auth API and the admin routes, trusted-types 'none') or 'other'.
 * @param {string} csp
 * @returns {'marketing' | 'admin' | 'other'}
 */
export function cspKind(csp) {
  const d = new Map();
  for (const part of csp.split(';')) {
    const [name, ...rest] = part.trim().split(/\s+/);
    if (name) d.set(name, rest);
  }
  const scripts = d.get('script-src') ?? [];
  const hashed = scripts.length > 0 && scripts.every((s) => /^'sha256-[A-Za-z0-9+/=]+'$/.test(s));
  if (!hashed || csp.includes('unsafe-')) return 'other';
  const connect = (d.get('connect-src') ?? []).join(' ');
  const types = (d.get('trusted-types') ?? []).join(' ');
  if (connect === "'none'" && !d.has('trusted-types')) return 'marketing';
  const two = /^https:\/\/\S+\/auth\/v1\/ https:\/\/\S+\/v1\/admin\/$/.test(connect);
  if (two && types === "'none'" && scripts.length === 1) return 'admin';
  return 'other';
}

/**
 * What is wrong with one response against `want` (the site's rules for every response included);
 * [] when nothing is.
 * @param {string} base
 * @param {RawResponse} res
 * @param {Want} want
 * @returns {string[]}
 */
export function responseProblems(base, res, want) {
  const problems = [];
  const redirect = res.status >= 300 && res.status < 400;
  if (!want.status.includes(res.status)) problems.push(`status ${res.status}`);

  const location = values(res, 'location');
  for (const l of location) {
    if (sitePath(base, l) === null) problems.push(`Location ${JSON.stringify(l)} leaves the site`);
  }
  if (!redirect && location.length) problems.push('a Location without a redirect');
  if (redirect && location.length !== 1) problems.push(`${location.length} Location headers`);
  if (redirect && location.length === 1 && !(want.location ?? []).includes(sitePath(base, location[0]))) {
    problems.push(`Location ${location[0]}, not ${(want.location ?? []).join(' or ') || 'expected'}`);
  }

  // Exactly once on every answer, at most once on a redirect.
  const count = (name, n) => {
    if (n > 1 || (n === 0 && !redirect)) problems.push(`${n} × ${name}`);
  };
  const csp = values(res, 'content-security-policy');
  count('Content-Security-Policy', csp.length);
  if (csp.length === 1 && cspKind(csp[0]) !== want.csp) {
    problems.push(`the ${cspKind(csp[0])} CSP, not the ${want.csp} one`);
  }
  for (const { key, value } of SHARED_HEADERS) {
    const got = values(res, key.toLowerCase());
    count(key, got.length);
    if (got.length === 1 && got[0] !== value) problems.push(`${key}: ${got[0]}`);
  }
  const cache = values(res, 'cache-control');
  count('Cache-Control', cache.length);
  if (!redirect && want.cache && cache.length === 1 && cache[0] !== want.cache) {
    problems.push(`Cache-Control: ${cache[0]}, not ${want.cache}`);
  }

  const robots = values(res, 'x-robots-tag');
  if (want.robots ? robots.join() !== 'noindex, nofollow' : robots.length) {
    problems.push(`X-Robots-Tag: ${JSON.stringify(robots)}`);
  }
  const type = values(res, 'content-type');
  if (want.type && (type.length !== 1 || !want.type.test(type[0]))) {
    problems.push(`Content-Type: ${JSON.stringify(type)}`);
  }
  if (values(res, 'set-cookie').length) problems.push('Set-Cookie');
  if (res.status === 404 && !res.body.toString('utf8').includes('Page not found')) {
    problems.push("not the site's 404 page");
  }
  if (want.body && !res.body.equals(want.body)) problems.push('a different body');
  return problems;
}

/**
 * The privacy version a /privacy page shows: the "Version {version}" line (messages/en.json →
 * legal.version, rendered by src/views/Legal.tsx), the first one on the page. Not the synthetics'
 * marker (deploy/grafana/synthetics: a sentence of the text), which says the page is up, not which
 * version it is.
 * @param {string} html
 * @returns {string | null}
 */
export function shownVersion(html, template = versionTemplate()) {
  const [before, after = ''] = template.split('{version}');
  const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = new RegExp(`${esc(before)}(\\d{4}-\\d{2}-\\d{2})${esc(after)}`).exec(html);
  return m ? m[1] : null;
}

function versionTemplate() {
  try {
    const t = JSON.parse(readFileSync(resolve(WEB, 'messages/en.json'), 'utf8')).legal.version;
    if (typeof t === 'string' && t.includes('{version}')) return t;
  } catch {
    // Run outside the repository: the template as it is today.
  }
  return 'Version {version}';
}

function oldestPrivacyVersion() {
  try {
    const list = JSON.parse(readFileSync(resolve(WEB, 'content/legal/privacy-versions.json'), 'utf8'));
    return /^\d{4}-\d{2}-\d{2}$/.test(list[0]) ? list[0] : null;
  } catch {
    return null;
  }
}

/** A response, or status 0 and the error when the request failed. */
async function get(origin, path) {
  try {
    return await fetchRaw(origin, path);
  } catch (e) {
    return { status: 0, headers: [], body: Buffer.alloc(0), error: String(e?.message ?? e) };
  }
}

/** What came back, for the PASS line: status, Location, Cache-Control, headers on a redirect. */
function summary(res) {
  if (res.error) return res.error;
  const parts = [String(res.status)];
  const location = values(res, 'location');
  if (location.length) parts.push(`→ ${location.join(' | ')}`);
  parts.push(`Cache-Control: ${values(res, 'cache-control').join(' | ') || 'none'}`);
  if (res.status >= 300 && res.status < 400) {
    const names = ['content-security-policy', ...SHARED_HEADERS.map((h) => h.key.toLowerCase())];
    const sent = names.filter((n) => values(res, n).length).length;
    parts.push(`security headers on the redirect: ${sent} of ${names.length}`);
  }
  return parts.join(', ');
}

/** Every check; returns the number that failed. */
export async function run({ base, www, policyVersion, appleCdn }) {
  let total = 0;
  let failed = 0;
  const report = (ok, name, detail) => {
    total++;
    if (!ok) failed++;
    console.log(`${ok ? 'PASS' : 'FAIL'} ${name}${detail ? ` (${detail})` : ''}`);
  };
  const probe = async (name, path, want) => {
    const res = await get(base, path);
    const problems = res.error ? [res.error] : responseProblems(base, res, want);
    report(problems.length === 0, name, problems.length ? problems.join('; ') : summary(res));
    return res;
  };
  console.log(`check-hosting: ${base}`);

  // The pages the app and App Store Connect link to, and the operator's page.
  for (const [path, cache] of [
    ['/', CACHE.pages],
    ['/privacy', CACHE.inForce],
    ['/impressum', CACHE.pages],
    ['/terms', CACHE.pages],
    ['/support', CACHE.pages],
  ]) {
    await probe(`${path} is a page`, path, { status: [200], csp: 'marketing', cache, type: HTML });
  }
  await probe('/admin is the operator page, under its own policy', '/admin', {
    status: [200],
    csp: 'admin',
    cache: CACHE.never,
    type: HTML,
    robots: true,
  });

  // Redirects: #156's trailing slash, the old German URLs (web D18), cleanUrls' .html.
  const redirects = [
    ['/de', '/'],
    ['/de/', '/de'],
    ['/de/privacy', '/privacy'],
    ['/privacy/', '/privacy'],
    ['/privacy.html', '/privacy'],
    ['/admin.html', '/admin'],
    ['/admin/', '/admin'],
  ];
  const oldest = oldestPrivacyVersion();
  if (oldest) redirects.push([`/de/privacy/${oldest}`, `/privacy/${oldest}`]);
  for (const [from, to] of redirects) {
    await probe(`${from} → 301 ${to}`, from, { status: [301], location: [to], csp: 'marketing' });
  }
  // A query string may stay or go (D19 records which); the path may not change.
  for (const [from, to] of [
    ['/de?x=1', '/'],
    ['/privacy/?x=1', '/privacy'],
  ]) {
    const want = { status: [301], location: [`${to}?x=1`, to], csp: 'marketing' };
    await probe(`${from} → 301 ${to}, with or without ?x=1`, from, want);
  }

  // Nothing leaves the site. The %5C paths are 404s; "//" may be merged into "/" first, and then
  // the trailing-slash rule answers with a path of this site.
  for (const [path, status, location] of [
    ['/x//', [301, 404], ['/x/', '/x']],
    ['//evil.example/', [301, 404], ['/evil.example']],
    ['/%5Cevil.example/', [404], []],
    ['/de/%5Cevil.example', [404], []],
  ]) {
    await probe(`${path} stays on the site`, path, { status, location, csp: 'marketing' });
  }
  await probe('an unknown path is the 404 page', '/no-such-page', {
    status: [404],
    csp: 'marketing',
    cache: CACHE.pages,
  });
  // The precompressed copies are not uploaded (ignore: **/*.gz).
  for (const path of ['/privacy.html.gz', '/index.html.gz', '/sitemap.xml.gz']) {
    await probe(`${path} is 404`, path, { status: [404], csp: 'marketing' });
  }

  // The AASA: Apple fetches it without following redirects.
  const aasa = await probe('the AASA: 200 application/json, no redirect', AASA, {
    status: [200],
    csp: 'marketing',
    cache: CACHE.aasa,
    type: JSON_TYPE,
  });
  const file = resolve(WEB, 'public', AASA.slice(1));
  if (existsSync(file)) {
    report(aasa.body.equals(readFileSync(file)), 'the AASA is the file in public/.well-known/');
  } else {
    let applinks = false;
    try {
      applinks = Boolean(JSON.parse(aasa.body.toString('utf8')).applinks);
    } catch {
      // not JSON: fails below
    }
    report(applinks, 'the AASA is JSON with applinks', 'public/ is not here to compare with');
  }

  // The app's universal links on a device without the app: open.html, never cached.
  const open = await probe('/open is the app-link page', '/open', {
    status: [200],
    csp: 'marketing',
    cache: CACHE.pages,
    type: HTML,
  });
  for (const path of [
    '/day/2026-10-19',
    '/sleep/plan',
    '/insights/behaviors',
    '/train/sessions/3f6c1a52-8a9e-4c1b-9d0e-2b7a4f1e9c33',
    '/train/history',
    '/invite/RYFT7Q2K',
    '/auth/callback',
  ]) {
    await probe(`${path} serves open.html, no-store`, path, {
      status: [200],
      csp: 'marketing',
      cache: CACHE.never,
      type: HTML,
      body: open.body,
    });
  }

  if (www) {
    const res = await get(www, '/privacy');
    const location = values(res, 'location');
    const ok = res.status >= 300 && res.status < 400 && location.join() === `${base}/privacy`;
    report(ok, `${www}/privacy → ${base}/privacy`, summary(res));
  }

  if (policyVersion) {
    const res = await get(base, '/privacy');
    const shown = res.error ? null : shownVersion(res.body.toString('utf8'));
    const detail = res.error ?? (shown ? `it shows ${shown}` : 'no version line found');
    report(shown === policyVersion, `/privacy shows version ${policyVersion}`, detail);
  }

  if (appleCdn) {
    const res = await get(APPLE_CDN, `/a/v1/${new URL(base).hostname}`);
    let same = false;
    try {
      const cdn = JSON.parse(res.body.toString('utf8'));
      same = isDeepStrictEqual(cdn, JSON.parse(aasa.body.toString('utf8')));
    } catch {
      // either is not JSON: not the same
    }
    report(res.status === 200 && same, "Apple's CDN holds the served AASA", summary(res));
  }

  if (failed) console.log(`check-hosting: ${failed} of ${total} checks failed`);
  else console.log(`check-hosting: all ${total} checks passed`);
  return failed;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (e) {
    console.error(`check-hosting: ${e.message}\n${USAGE}`);
    process.exit(2);
  }
  if (opts.help) {
    console.log(USAGE);
    process.exit(0);
  }
  const failed = await run(opts);
  process.exit(failed ? 1 : 0);
}

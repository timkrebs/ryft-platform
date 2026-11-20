// firebase.json for Firebase Hosting (web D19): what scripts/firebase-json.mjs writes after the
// build, from the export's script hashes and the policies of ./csp.mjs, so the CSP stays one
// source. Pure and dependency-free: tests/hosting.test.ts calls it
// with fixture hashes, and scripts/check-hosting.mjs reads the header values from here.
//
// Hosting has no locations. Every `headers` rule whose pattern matches the REQUEST PATH applies, on
// every status (a 404 gets the headers of its path, not those of 404.html). How Hosting combines two
// matching rules that set the same header is not documented, so:
//  - the two CSP rules are disjoint: the marketing rule matches every path except exactly /admin
//    (written without lookaround, which RE2 lacks), the admin rule /admin alone;
//  - every other header except Cache-Control is set by one rule only;
//  - the Cache-Control rules go from general to specific (reported: the last matching rule wins).
// Every pattern is a `regex` in RE2's syntax, anchored ^…$, so it means the same whether Hosting
// matches the whole path or searches it. ANY is any character, a line break included: `.` stops at
// \n in RE2 and in JavaScript, and a decoded path may hold one.
import { adminPolicy, policy } from './csp.mjs';

const ANY = '[\\s\\S]';
// #156: what a redirect may put into Location. It starts with a letter or digit, so Location never
// begins with "//" or "/\" (another host to a browser), and holds no backslash, space or control
// character.
const SAFE = '[A-Za-z0-9][A-Za-z0-9/._-]*';

/** The first path segments of the app's universal links (the AASA); a device without the app gets open.html. */
export const APP_LINKS = ['day', 'sleep', 'insights', 'train', 'auth', 'invite'];

/** Stable names in public/ (icons, screenshots, OG images, manifest, sitemap, robots.txt): a day. */
export const DAY_EXTENSIONS = ['png', 'svg', 'avif', 'webp', 'ico', 'webmanifest', 'xml', 'txt'];

const PERMISSIONS = [
  'accelerometer',
  'camera',
  'geolocation',
  'gyroscope',
  'microphone',
  'payment',
  'usb',
  'browsing-topics',
];

/** The headers every response carries, whichever policy it has (tests/hosting.test.ts pins them). */
export const SHARED_HEADERS = [
  { key: 'X-Content-Type-Options', value: 'nosniff' },
  { key: 'Referrer-Policy', value: 'no-referrer' },
  { key: 'X-Frame-Options', value: 'DENY' },
  { key: 'Strict-Transport-Security', value: 'max-age=31536000; includeSubDomains' },
  { key: 'Cross-Origin-Opener-Policy', value: 'same-origin' },
  { key: 'Cross-Origin-Resource-Policy', value: 'same-origin' },
  { key: 'Permissions-Policy', value: PERMISSIONS.map((p) => `${p}=()`).join(', ') },
];

/** Cache-Control per path class, as nginx sent it. */
export const CACHE = {
  // Every other path: the pages (and the 404 page under the path that was asked for).
  pages: 'public, max-age=300',
  // Content-hashed CSS and fonts.
  hashed: 'public, max-age=31536000, immutable',
  files: 'public, max-age=86400',
  // The old German URLs' 301s (web D18): an hour, not forever, so the paths stay free.
  german: 'public, max-age=3600',
  aasa: 'public, max-age=3600',
  // /privacy shows the text in force: revalidated after every deploy.
  inForce: 'no-cache',
  // The app links (auth callbacks carry tokens, invite links a code) and /admin.
  never: 'no-store',
};

/**
 * A pattern for every string except exactly `word` (lowercase letters), without lookaround: at each
 * position either the string ends, or it has another character there, or it has the word's
 * character and goes on; after the whole word, at least one more character.
 */
export function allExcept(word) {
  if (!/^[a-z]+$/.test(word)) {
    throw new Error(`allExcept: ${JSON.stringify(word)} is not lowercase letters`);
  }
  let tail = `${ANY}+`;
  for (const c of [...word].reverse()) tail = `(?:|[^${c}]${ANY}*|${c}${tail})`;
  return tail;
}

/** A RE2 pattern of this file as a JavaScript RegExp (RE2's (?P<name>…) is JavaScript's (?<name>…)). */
export function jsRegExp(pattern) {
  return new RegExp(pattern.replaceAll('(?P<', '(?<'));
}

const header = (key, value) => ({ key, value });
const cache = (regex, value) => ({ regex, headers: [header('Cache-Control', value)] });

/**
 * firebase.json. `hashes`: the marketing pages' inline-script hashes (none since web D20, so their
 * policy is script-src 'none'); `adminHashes`: the admin page's one; `config`: config/admin.json
 * (the admin policy's connect-src).
 */
export function firebaseJson({ hashes, adminHashes, config }) {
  return {
    hosting: {
      public: 'out',
      cleanUrls: true,
      // Only precompressed copies: the build makes none (Hosting compresses on its own, web D19),
      // and a stray *.gz is never uploaded. Never "**/.*", firebase init's default: it would drop
      // .well-known/ and the AASA with it.
      ignore: ['**/*.gz'],
      // Before any file is looked up. 1: #156's trailing slash, the only one (trailingSlash is
      // unset, so Hosting redirects no other). 2: bare /de as an exact rule, so it never depends
      // on how Hosting fills a capture that did not match. 3: /de/<path> to the English page.
      redirects: [
        { regex: `^/(?P<p>${SAFE})/$`, destination: '/:p', type: 301 },
        { source: '/de', destination: '/', type: 301 },
        { regex: `^/de/(?P<p>${SAFE})$`, destination: '/:p', type: 301 },
      ],
      rewrites: [{ regex: `^/(${APP_LINKS.join('|')})(/.*)?$`, destination: '/open.html' }],
      headers: [
        { regex: `^/${ANY}*$`, headers: SHARED_HEADERS.map((h) => ({ ...h })) },
        {
          regex: `^/${allExcept('admin')}$`,
          headers: [header('Content-Security-Policy', policy(hashes))],
        },
        cache(`^/${ANY}*$`, CACHE.pages),
        cache(`^/_next/static/${ANY}*$`, CACHE.hashed),
        // After /_next/static/ and before /de and the app links, as nginx's locations ranked them.
        cache(`^/${ANY}*\\.(?:${DAY_EXTENSIONS.join('|')})$`, CACHE.files),
        // Exactly the paths redirects 2 and 3 answer.
        cache(`^/de(?:/${SAFE})?$`, CACHE.german),
        cache(`^/(?:${APP_LINKS.join('|')})(?:/${ANY}*)?$`, CACHE.never),
        cache('^/privacy$', CACHE.inForce),
        {
          regex: '^/\\.well-known/apple-app-site-association$',
          headers: [header('Content-Type', 'application/json'), header('Cache-Control', CACHE.aasa)],
        },
        // The operator's page (web D15): its own policy, never cached, never indexed. Last, so
        // its Cache-Control is the one that wins.
        {
          regex: '^/admin$',
          headers: [
            header('Content-Security-Policy', adminPolicy(adminHashes, config)),
            header('Cache-Control', CACHE.never),
            header('X-Robots-Tag', 'noindex, nofollow'),
          ],
        },
      ],
    },
    emulators: {
      // localhost: what Playwright, linkinator and Lighthouse ask for (http://localhost:8080).
      hosting: { host: 'localhost', port: 8080 },
      ui: { enabled: false },
    },
  };
}

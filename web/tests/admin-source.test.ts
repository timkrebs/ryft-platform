import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { bundleAdmin } from '../scripts/lib/admin-bundle.mjs';
import config from '../config/admin.json';

// The admin page's rules, enforced on its source and its bundle (web D15–D17). ESLint and
// Trusted Types 'none' enforce the same sinks; this is the third check.
function* files(dir: string): Generator<string> {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* files(p);
    else yield p;
  }
}
const SRC = [...files('src/admin')].filter((f) => f.endsWith('.ts'));
const code = (f: string) =>
  readFileSync(f, 'utf8')
    .split('\n')
    .filter((l) => !/^\s*(\/\/|\*|\/\*)/.test(l))
    .join('\n');
const text = Object.fromEntries(SRC.map((f) => [f, code(f)]));

describe('src/admin source', () => {
  it('has the files it should', () => {
    expect(SRC.length).toBeGreaterThan(10);
  });

  it('has no HTML sink, no eval, no inline-style or cookie use, and no console', () => {
    const banned: [string, RegExp][] = [
      ['innerHTML', /\binnerHTML\b/],
      ['outerHTML', /\bouterHTML\b/],
      ['insertAdjacentHTML', /\binsertAdjacentHTML\b/],
      ['document.write', /\bdocument\.write(ln)?\b/],
      ['srcdoc', /\.srcdoc\b/],
      ['eval', /\beval\s*\(/],
      ['new Function', /\bnew\s+Function\b/],
      ['a string timer', /\bset(Timeout|Interval)\s*\(\s*['"`]/],
      ['dynamic import', /\bimport\s*\(/],
      ['dangerouslySetInnerHTML', /dangerouslySetInnerHTML/],
      ['inline style', /\.style\b|setAttribute\(\s*['"]style/],
      ['an on* attribute', /setAttribute\(\s*['"]on/i],
      ['localStorage', /\blocalStorage\b/],
      ['a cookie', /\bdocument\.cookie\b/],
      ['console', /\bconsole\./],
      ['XMLHttpRequest', /\bXMLHttpRequest\b/],
      ['WebSocket', /\bWebSocket\b/],
      ['sendBeacon', /\bsendBeacon\b/],
      ['window.open', /\bwindow\.open\b/],
    ];
    const hits = SRC.flatMap((f) =>
      banned.filter(([, re]) => re.test(text[f]!)).map(([what]) => `${f}: ${what}`),
    );
    expect(hits).toEqual([]);
  });

  it('reads the URL only for its own #view', () => {
    const hits = SRC.flatMap((f) =>
      [
        ...text[f]!.matchAll(/\blocation\b(?!\.hash\b)|document\.URL|\bdocument\.referrer\b|\bhistory\b/g),
      ].map((m) => `${f}: ${m[0]}`),
    );
    expect(hits).toEqual([]);
  });

  it('uses the Navigation API and pagehide only in leave.ts (the session ends when the tab leaves /admin)', () => {
    const hits = SRC.filter((f) => !f.endsWith('leave.ts')).flatMap((f) =>
      [...text[f]!.matchAll(/\bnavigation\b|\bpagehide\b|\bbeforeunload\b|\bunload\b/g)].map(
        (m) => `${f}: ${m[0]}`,
      ),
    );
    expect(hits).toEqual([]);
    expect(text['src/admin/app.ts']).toMatch(
      /clearOnLeave\(window as unknown as WindowLike, \(\) => sessions\.clear\(\)\)/,
    );
  });

  it('builds DOM through dom.ts only: no createElement outside it', () => {
    const hits = SRC.filter((f) => !f.endsWith('dom.ts')).filter((f) => /\bcreateElement\b/.test(text[f]!));
    expect(hits).toEqual([]);
  });

  it('calls exactly the routes an operator token may reach, and none of the key-only ones', () => {
    const allowed = new Set([
      'GET /v1/admin/invites',
      'POST /v1/admin/invites',
      'DELETE /v1/admin/invites/{code}',
      'GET /v1/admin/users',
      'POST /v1/admin/users/{id}/access',
      'DELETE /v1/admin/users/{id}/access',
      'GET /v1/admin/feedback',
      'POST /v1/admin/feedback/{id}/resolve',
      'GET /v1/admin/flags',
      'GET /v1/admin/audit',
    ]);
    const found = new Set<string>();
    for (const f of SRC) {
      for (const m of text[f]!.matchAll(
        /api\.(get|post|del)(?:<[^>]*>)?\(\s*[`'"](\/v1\/admin\/[^`'"?]*)/g,
      )) {
        const verb = { get: 'GET', post: 'POST', del: 'DELETE' }[m[1]!];
        const path = m[2]!
          .replace(/\$\{q\}/g, '')
          .replace(/\$\{[^}]*\}/g, (s) => (/\.code\b/.test(s) ? '{code}' : '{id}'));
        found.add(`${verb} ${path}`);
      }
    }
    expect([...found].filter((r) => !allowed.has(r))).toEqual([]);
    // Everything the page needs is called (users with ${...}/access in both directions, etc.).
    expect(found.size).toBe(allowed.size);
    expect(SRC.map((f) => text[f]).join('\n')).not.toMatch(/merge-into|resync-hint|\/flags\/|api\.put/);
  });

  it('renders only the fields an operator token gets (AdminUserBrief, FeedbackBrief)', () => {
    const props = (file: string, v: string) =>
      new Set([...text[file]!.matchAll(new RegExp(`\\b${v}\\.([a-z_0-9]+)`, 'g'))].map((m) => m[1]));
    const users = props('src/admin/screens/users.ts', 'u');
    expect(
      [...users].filter(
        (p) =>
          !['id', 'short_id', 'created_at', 'provider', 'email', 'beta_access', 'last_sync_at'].includes(p!),
      ),
    ).toEqual([]);
    const feedback = props('src/admin/screens/feedback.ts', 'f');
    expect(
      [...feedback].filter(
        (p) => !['id', 'user_short_id', 'category', 'message', 'created_at', 'resolved_at'].includes(p!),
      ),
    ).toEqual([]);
    // Flags show the count of allow-listed accounts, never their ids.
    expect(text['src/admin/screens/flags.ts']!.match(/allow_user_ids/g)).toHaveLength(1);
    expect(text['src/admin/screens/flags.ts']).toContain('allow_user_ids?.length');
    const all = SRC.map((f) => text[f]).join('\n');
    for (const field of [
      'consent_version',
      'days_with_hrv_14d',
      'nights_with_stages_14d',
      'days_scored_14d',
      'calibrating',
      'diagnostics',
      'request_ids',
      'device_model',
      'os_version',
      'app_version',
      'max_hr_source',
    ]) {
      expect(all, field).not.toMatch(new RegExp(`\\b${field}`));
    }
  });
});

describe('the admin bundle', async () => {
  const bundle = await bundleAdmin();

  it('is one small script that can sit inside a <script> element', () => {
    expect(bundle.length).toBeGreaterThan(5_000);
    expect(bundle.length).toBeLessThan(60_000);
    expect(bundle).not.toMatch(/<\/script/i);
    expect(bundle).not.toContain('<!--');
    expect(
      bundle.startsWith('"use strict"') || bundle.startsWith('(()=>{') || bundle.startsWith('(function'),
    ).toBe(true);
  });

  it('holds no third-party code and no URL but the two configured origins', () => {
    expect(bundle).not.toMatch(/supabase-js|auth-js|gotrue-js|@supabase/);
    // The two origins, plus the constant prefix of an invite link (a link is shared only if it starts so).
    const allowed = new Set([config.apiUrl, config.supabaseUrl, 'https://ryftlab.de']);
    const urls = new Set([...bundle.matchAll(/https?:\/\/[^"'`\s\\)]+/g)].map((m) => m[0]));
    expect([...urls].filter((u) => !allowed.has(u) && !allowed.has(new URL(u).origin))).toEqual([]);
  });

  it('reads the URL only for #view and uses the Navigation API only for the leave guard', () => {
    // Every `location` is `location.hash`; no other way to learn the URL or the referrer.
    expect([...bundle.matchAll(/\blocation\b(?!\.hash\b)/g)]).toEqual([]);
    expect(bundle).toContain('location.hash');
    const stripped = bundle.replace(/referrerPolicy|no-referrer/g, '');
    for (const re of [/document\.URL/, /\bbaseURI\b/, /\bdocumentURI\b/, /\breferrer\b/, /\bhistory\b/]) {
      expect(stripped, String(re)).not.toMatch(re);
    }
    // Exactly the one use in leave.ts: window.navigation, its navigate event and pagehide.
    expect(bundle.match(/\bnavigation\b/g)).toHaveLength(2);
    expect(bundle.match(/\bpagehide\b/g)).toHaveLength(1);
    expect(bundle.match(/["']navigate["']/g)).toHaveLength(1);
  });

  it('keeps the session in sessionStorage and uses no other store', () => {
    expect(bundle).toContain('sessionStorage');
    expect(bundle).not.toContain('localStorage');
    expect(bundle).not.toContain('document.cookie');
  });
});

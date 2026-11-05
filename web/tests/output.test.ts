import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { gzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';
import fonts from '../config/fonts.json';
import { cspHash } from '../scripts/lib/html.mjs';
import { THEME_INIT, THEME_SWITCH } from '../src/lib/inline-scripts';

// Checks on the built site (out/). Skipped before a build unless REQUIRE_OUT=1 (CI).
const OUT = 'out';
const built = existsSync(join(OUT, 'index.html'));
if (!built && process.env.REQUIRE_OUT) throw new Error('out/ is missing: run `npm run build` first');

function* files(dir: string): Generator<string> {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* files(p);
    else yield p;
  }
}

const isAdminPage = (f: string) => relative(OUT, f) === 'admin.html';

describe.skipIf(!built)('built site', () => {
  const html = built ? [...files(OUT)].filter((f) => f.endsWith('.html')) : [];

  it('ships no script except our own inline ones', () => {
    for (const f of html) {
      const scripts = [...readFileSync(f, 'utf8').matchAll(/<script\b([^>]*)>/g)].map((m) => m[1]);
      for (const attrs of scripts) {
        expect(attrs, f).toMatch(/data-ryft/);
        expect(attrs, f).not.toMatch(/\bsrc=/);
      }
    }
    expect([...files(OUT)].filter((f) => f.endsWith('.js'))).toEqual([]);
  });

  it('keeps / under the 80 KB gzipped JavaScript budget', () => {
    const page = readFileSync(join(OUT, 'index.html'), 'utf8');
    const js = [...page.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)].map((m) => m[1]).join('');
    expect(gzipSync(js).length).toBeLessThan(80 * 1024);
  });

  it('serves Satoshi byte-identical to the pinned files', () => {
    const pinned = new Set(Object.values(fonts.files).map((f) => f.sha256));
    const served = [...files(OUT)].filter((f) => f.endsWith('.woff2'));
    expect(served.length).toBe(3);
    for (const f of served) {
      expect(pinned.has(createHash('sha256').update(readFileSync(f)).digest('hex')), f).toBe(true);
    }
  });

  it('has no inline style attributes or <style> elements', () => {
    for (const f of html) {
      const page = readFileSync(f, 'utf8');
      expect(page, f).not.toMatch(/<style\b/);
      expect(page.replace(/<svg[\s\S]*?<\/svg>/g, ''), f).not.toMatch(/\sstyle="/);
    }
  });

  it('runs no script that could read the URL (auth callbacks carry tokens)', () => {
    // The one exception, narrow (web D15): /admin's own bundle reads its #view from the fragment
    // and is checked on its own below. Every other page keeps the strict rule.
    for (const f of html.filter((f) => !isAdminPage(f))) {
      for (const [, body] of readFileSync(f, 'utf8').matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)) {
        expect(body, f).not.toMatch(/location|document\.URL|\bhref\b|\bhash\b|\bsearch\b|referrer|history/);
      }
    }
  });

  it('pins the marketing CSP: script-src is exactly the hashes of the two theme scripts', () => {
    // scripts/csp.mjs hashes every inline data-ryft script it finds, so a new script on any page
    // (or the admin bundle leaking into this policy) would widen it; this fails instead.
    const csp = readFileSync('nginx/csp.conf', 'utf8');
    const scriptSrc = /script-src ([^;]*);/.exec(csp)![1]!;
    expect(scriptSrc.split(' ').sort()).toEqual([cspHash(THEME_INIT), cspHash(THEME_SWITCH)].sort());
    expect(csp).toContain("connect-src 'none'");
  });

  describe('the admin page (the /admin exception, web D15)', () => {
    const page = () => readFileSync(join(OUT, 'admin.html'), 'utf8');
    const otherPages = () => html.filter((f) => !isAdminPage(f));

    it('has exactly one script, the bundle, and no other page carries it', () => {
      const scripts = [...page().matchAll(/<script\b([^>]*)>/g)].map((m) => m[1]!);
      expect(scripts).toHaveLength(1);
      expect(scripts[0]).toMatch(/data-ryft="admin"/);
      expect(scripts[0]).not.toMatch(/\bsrc=/);
      for (const f of otherPages()) expect(readFileSync(f, 'utf8'), f).not.toContain('data-ryft="admin"');
    });

    it('is noindex by meta tag and its own document: no theme scripts, no alternates', () => {
      const p = page();
      expect(p).toMatch(/<meta name="robots" content="noindex, nofollow"/);
      expect(p).not.toMatch(/data-ryft="theme-/);
      expect(p).not.toMatch(/<link rel="(alternate|canonical)"/);
      expect(p).toContain('<html lang="en"');
    });

    it('is not in the sitemap or robots.txt (that would list the path), and no page links to it', () => {
      expect(readFileSync(join(OUT, 'sitemap.xml'), 'utf8')).not.toMatch(/\/admin/);
      expect(readFileSync(join(OUT, 'robots.txt'), 'utf8')).not.toMatch(/admin/i);
      for (const f of otherPages()) expect(readFileSync(f, 'utf8'), f).not.toMatch(/href="\/admin/);
    });

    it('names its connections in the footer', () => {
      expect(page()).toContain(
        'Signs in with Supabase Auth; talks to api.ryftapp.de. Nothing else is loaded.',
      );
    });

    it('has its hash in the admin policy only, and the marketing policy keeps connect-src none', () => {
      const csp = readFileSync('nginx/csp.conf', 'utf8');
      const adminCsp = readFileSync('nginx/csp-admin.conf', 'utf8');
      const body = [...page().matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)][0]![1]!;
      const hash = `'sha256-${createHash('sha256').update(body, 'utf8').digest('base64')}'`;
      expect(adminCsp).toContain(`script-src ${hash};`);
      expect(csp).not.toContain(hash);
      expect(adminCsp).toMatch(
        /connect-src https:\/\/[a-z0-9]+\.supabase\.co\/auth\/v1\/ https:\/\/api\.ryftapp\.de\/v1\/admin\/;/,
      );
      expect(csp).toContain("connect-src 'none'");
    });
  });

  it('keeps the app-link fallback, drafts, the styleguide and 404 out of search engines', () => {
    const noindex = (f: string) =>
      /<meta name="robots" content="noindex/.test(readFileSync(join(OUT, f), 'utf8'));
    for (const f of ['open.html', '404.html', 'styleguide.html']) expect(noindex(f), f).toBe(true);
    for (const f of ['privacy.html', 'de/privacy.html', 'terms.html', 'impressum.html', 'support.html']) {
      const page = readFileSync(join(OUT, f), 'utf8');
      if (/Draft — not yet in force|Entwurf – noch nicht in Kraft/.test(page))
        expect(noindex(f), f).toBe(true);
    }
    const sitemap = readFileSync(join(OUT, 'sitemap.xml'), 'utf8');
    for (const p of ['/styleguide', '/open', '/404']) expect(sitemap).not.toContain(`ryftapp.de${p}<`);
    expect(sitemap).toContain('<loc>https://ryftapp.de/de/science</loc>');
  });

  it('links every translated page to its other language', () => {
    for (const f of [
      'index.html',
      'science.html',
      'beta.html',
      'privacy.html',
      'de.html',
      'de/privacy.html',
    ]) {
      const page = readFileSync(join(OUT, f), 'utf8');
      expect(page, f).toMatch(/<link rel="alternate" hrefLang="de"/);
      expect(page, f).toMatch(/<link rel="alternate" hrefLang="en"/);
    }
  });

  it('lists the app’s and the site’s components with their licences on /licenses', () => {
    const page = readFileSync(join(OUT, 'licenses.html'), 'utf8');
    for (const name of [
      'supabase-swift',
      'swift-crypto',
      '>next<',
      '>react<',
      'Satoshi',
      'ITF Free Font License',
    ]) {
      expect(page).toContain(name);
    }
    expect(page).toContain('Permission is hereby granted');
  });
});

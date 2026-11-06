import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { gzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';

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

  it('ships no script at all, except the admin bundle (web D15, D19)', () => {
    for (const f of html) {
      const scripts = [...readFileSync(f, 'utf8').matchAll(/<script\b([^>]*)>/g)].map((m) => m[1]);
      if (isAdminPage(f)) continue;
      expect(scripts, f).toEqual([]);
    }
    expect([...files(OUT)].filter((f) => f.endsWith('.js'))).toEqual([]);
  });

  it('keeps / under the 80 KB gzipped JavaScript budget', () => {
    const page = readFileSync(join(OUT, 'index.html'), 'utf8');
    const js = [...page.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)].map((m) => m[1]).join('');
    expect(gzipSync(js).length).toBeLessThan(80 * 1024);
  });

  it('serves Inter Regular and Bold byte-identical to the committed files, preloaded', () => {
    const committed = new Set(
      ['Inter-Regular.woff2', 'Inter-Bold.woff2'].map((f) =>
        createHash('sha256')
          .update(readFileSync(join('src/fonts', f)))
          .digest('hex'),
      ),
    );
    const served = [...files(OUT)].filter((f) => f.endsWith('.woff2'));
    expect(served.length).toBe(2);
    for (const f of served) {
      expect(committed.has(createHash('sha256').update(readFileSync(f)).digest('hex')), f).toBe(true);
    }
    const page = readFileSync(join(OUT, 'index.html'), 'utf8');
    // Both preloaded, and the fallback is the hand-written metric-matched one (D20).
    expect(page.match(/<link rel="preload"[^>]*as="font"/g) ?? []).toHaveLength(2);
    const css = [...files(OUT)]
      .filter((f) => f.endsWith('.css'))
      .map((f) => readFileSync(f, 'utf8'))
      .join('');
    expect(css).toMatch(/font-family:\s*["']?Inter Fallback/);
    expect(page).not.toMatch(/Satoshi/i);
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

  it("pins the marketing CSP: script-src 'none'", () => {
    // scripts/csp.mjs hashes every inline data-ryft script it finds, so a new script on any page
    // (or the admin bundle leaking into this policy) would widen it; this fails instead.
    const conf = readFileSync('nginx/csp.conf', 'utf8');
    const csp = conf.match(/add_header Content-Security-Policy "([^"]*)" always;/)?.[1] ?? '';
    const directive = (name: string) =>
      csp
        .split('; ')
        .filter((d) => d === name || d.startsWith(`${name} `))
        .map((d) => d.slice(name.length).trim());
    expect(directive('script-src')).toEqual(["'none'"]);
    expect(directive('connect-src')).toEqual(["'none'"]);
  });

  it('is light only: one theme-color, no theme attribute, no dark screenshot (web D19)', () => {
    for (const f of html.filter((f) => !isAdminPage(f))) {
      const page = readFileSync(f, 'utf8');
      expect(page.match(/<meta name="theme-color"/g) ?? [], f).toHaveLength(1);
      expect(page, f).not.toMatch(/data-theme|prefers-color-scheme|-dark(-\d+)?\.(png|avif|webp)/);
    }
    const css = [...files(OUT)]
      .filter((f) => f.endsWith('.css'))
      .map((f) => readFileSync(f, 'utf8'))
      .join('');
    expect(css).not.toMatch(/data-theme|prefers-color-scheme/);
    expect(css).toContain('color-scheme:light');
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

    it('is noindex by meta tag and its own document: no alternates', () => {
      const p = page();
      expect(p).toMatch(/<meta name="robots" content="noindex, nofollow"/);
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
    for (const f of ['privacy.html', 'terms.html', 'impressum.html', 'support.html']) {
      const page = readFileSync(join(OUT, f), 'utf8');
      if (/Draft — not yet in force/.test(page)) expect(noindex(f), f).toBe(true);
    }
    const sitemap = readFileSync(join(OUT, 'sitemap.xml'), 'utf8');
    for (const p of ['/styleguide', '/open', '/404']) expect(sitemap).not.toContain(`ryftapp.de${p}<`);
    expect(sitemap).toContain('<loc>https://ryftapp.de/science</loc>');
  });

  it('is English only: no German pages, no hreflang alternates (web D18)', () => {
    expect(existsSync(join(OUT, 'de.html'))).toBe(false);
    expect(existsSync(join(OUT, 'de'))).toBe(false);
    for (const f of ['index.html', 'science.html', 'privacy.html']) {
      const page = readFileSync(join(OUT, f), 'utf8');
      expect(page, f).not.toMatch(/hrefLang=/i);
      expect(page, f).toContain('<html lang="en"');
      expect(page, f).toMatch(/<link rel="canonical" href="https:\/\/ryftapp\.de[/"]/);
    }
    expect(readFileSync(join(OUT, 'sitemap.xml'), 'utf8')).not.toContain('/de');
  });

  it('lists the app’s and the site’s components with their licences on /licenses', () => {
    const page = readFileSync(join(OUT, 'licenses.html'), 'utf8');
    for (const name of [
      'supabase-swift',
      'swift-crypto',
      '>next<',
      '>react<',
      '>Inter<',
      'SIL Open Font License 1.1',
    ]) {
      expect(page).toContain(name);
    }
    expect(page).toContain('Permission is hereby granted');
    expect(page).toContain('SIL OPEN FONT LICENSE Version 1.1');
  });
});

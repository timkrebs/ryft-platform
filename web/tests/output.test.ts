import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { gzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';
import fonts from '../config/fonts.json';

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
    for (const f of html) {
      for (const [, body] of readFileSync(f, 'utf8').matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g)) {
        expect(body, f).not.toMatch(/location|document\.URL|\bhref\b|\bhash\b|\bsearch\b|referrer|history/);
      }
    }
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

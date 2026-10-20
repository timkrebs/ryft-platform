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
});

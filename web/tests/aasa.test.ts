import { existsSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

// public/.well-known/apple-app-site-association: valid, for the right app, and covering
// every universal link the app creates (AppRoute.swift → `var url`, the push table's
// `session` form, and Supabase's auth callback).
const AASA_PATH = 'public/.well-known/apple-app-site-association';
const APP = resolve(process.env.RYFT_APP_DIR ?? '../app');
const PLATFORM = resolve(process.env.RYFT_PLATFORM_DIR ?? '../plattform');
const ROUTE = join(APP, 'Ryft/Navigation/AppRoute.swift');
const SUPABASE = join(APP, 'Ryft/App/SupabaseConfig.swift');
const PLATFORM_AASA = join(PLATFORM, 'deploy/k8s/base/web/site/.well-known/apple-app-site-association');
const hasApp = existsSync(ROUTE);
if (!hasApp && process.env.REQUIRE_SIBLINGS) throw new Error(`${ROUTE} not found`);

type Component = { '/': string; comment?: string; exclude?: boolean };
const raw = readFileSync(AASA_PATH, 'utf8');
const aasa = JSON.parse(raw) as {
  applinks: { details: { appIDs: string[]; components: Component[] }[] };
  webcredentials?: { apps: string[] };
};
const components = aasa.applinks.details.flatMap((d) => d.components);

/** Apple's component matching: `*` any run of characters (also `/`), `?` one character. */
function matches(pattern: string, path: string): boolean {
  const re = pattern
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*/g, '.*')
    .replace(/\?/g, '.');
  return new RegExp(`^${re}$`).test(path);
}
const covered = (path: string) => components.some((c) => !c.exclude && matches(c['/'], path));

const SAMPLE: Record<string, string> = {
  DayFormat: '2026-10-19',
  id: '3f6c1a52-8a9e-4c1b-9d0e-2b7a4f1e9c33',
  m: 'hrv',
};

/** The paths `AppRoute.url` produces, with sample values for the interpolations. */
function generatedPaths(swift: string): string[] {
  const block = swift.slice(swift.indexOf('var url: URL?'), swift.indexOf('var needsSession'));
  return [...block.matchAll(/path = "([^"]*)"/g)]
    .map((m) => m[1])
    .filter(Boolean)
    .map((p) =>
      p.replace(/\\\(([^)]*\)?)\)/g, (_, expr: string) => {
        if (expr.includes('DayFormat')) return SAMPLE.DayFormat;
        if (expr.includes('uuidString')) return SAMPLE.id;
        return SAMPLE.m;
      }),
    )
    .map((p) => `/${p}`);
}

describe('apple-app-site-association', () => {
  it('is JSON with no file extension, for dev.timkrebs.ryft', () => {
    expect(AASA_PATH.endsWith('apple-app-site-association')).toBe(true);
    const ids = aasa.applinks.details.flatMap((d) => d.appIDs);
    expect(ids.length).toBeGreaterThan(0);
    for (const id of ids) expect(id).toMatch(/^[A-Z0-9]{10}\.dev\.timkrebs\.ryft$/);
    expect(aasa.webcredentials?.apps).toEqual(ids);
  });

  it('covers the app-link prefixes the site falls back for', () => {
    for (const p of [
      '/day/2026-10-19',
      '/sleep/plan',
      '/insights/behaviors',
      '/train/history',
      '/auth/callback',
    ]) {
      expect(covered(p), p).toBe(true);
    }
  });

  it.skipIf(!existsSync(PLATFORM_AASA))("is byte-identical to the platform's file it replaces", () => {
    expect(raw).toBe(readFileSync(PLATFORM_AASA, 'utf8'));
  });

  it.skipIf(!hasApp)('matches every universal link AppRoute creates', () => {
    const paths = generatedPaths(readFileSync(ROUTE, 'utf8'));
    expect(paths.length).toBeGreaterThanOrEqual(8);
    // The push table writes /train/session/<id> (AppRoute's doc comment).
    paths.push(`/train/session/${SAMPLE.id}`);
    const missing = paths.filter((p) => !covered(p));
    expect(missing).toEqual([]);
  });

  it.skipIf(!hasApp)('covers the auth callback the app registers with Supabase', () => {
    const url = readFileSync(SUPABASE, 'utf8').match(/universalRedirectURL = URL\(string: "([^"]+)"\)/)?.[1];
    expect(url).toBeDefined();
    expect(covered(new URL(url!).pathname)).toBe(true);
  });

  it.skipIf(!hasApp)("lists only first segments AppRoute's parser handles", () => {
    const swift = readFileSync(ROUTE, 'utf8');
    const handled = new Set([
      ...[...swift.matchAll(/case \("(\w+)"/g)].map((m) => m[1]),
      ...[...swift.matchAll(/parts\[0\] == "(\w+)"/g)].map((m) => m[1]),
      ...[...swift.matchAll(/parts\.first == "(\w+)"/g)].map((m) => m[1]),
    ]);
    for (const c of components) expect(handled, c['/']).toContain(c['/'].split('/')[1]);
  });
});

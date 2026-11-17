import { existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import sharp from 'sharp';
import { describe, expect, it } from 'vitest';
import {
  APP_ICON_SET,
  appDir,
  appIcon,
  hasAppIconSet,
  readRecord,
  sha256,
} from '../scripts/lib/app-icon.mjs';

// The site's icons and OG card come from the app's icon (M1, web D22): the largest PNG of its
// AppIcon set. config/app-icon.json records which file and its SHA-256, written by
// `npm run icons` (path, sha256) and `npm run og` (ogSha256).

type Entry = { filename?: string; size?: string; scale?: string; appearances?: object[] };

/** An app with an AppIcon set of these entries; `files` are written, the rest stay missing. */
function app(images: Entry[], files = images.map((i) => i.filename).filter(Boolean) as string[]) {
  const dir = mkdtempSync(join(tmpdir(), 'ryft-app-'));
  mkdirSync(join(dir, APP_ICON_SET), { recursive: true });
  writeFileSync(join(dir, APP_ICON_SET, 'Contents.json'), JSON.stringify({ images }));
  for (const f of files) writeFileSync(join(dir, APP_ICON_SET, f), `png ${f}`);
  return dir;
}
const dark = [{ appearance: 'luminosity', value: 'dark' }];

describe('appIcon() picks the source', () => {
  it('takes the largest image, in pixels (points × scale)', () => {
    const dir = app([
      { filename: 'small.png', size: '60x60', scale: '3x' },
      { filename: 'store.png', size: '1024x1024' },
      { filename: 'mid.png', size: '512x512', scale: '1x' },
    ]);
    const icon = appIcon(dir);
    expect(icon.path).toBe(`${APP_ICON_SET}/store.png`);
    expect(icon.pixels).toBe(1024);
    expect(icon.sha256).toBe(sha256(Buffer.from('png store.png')));
  });

  it('prefers the entry without appearances when the sizes tie, wherever it is listed', () => {
    const dir = app([
      { filename: 'dark.png', size: '1024x1024', appearances: dark },
      { filename: 'any.png', size: '1024x1024' },
    ]);
    expect(appIcon(dir).path).toBe(`${APP_ICON_SET}/any.png`);
  });

  it('fails when the set or the file is missing, or a size is unreadable', () => {
    const empty = mkdtempSync(join(tmpdir(), 'ryft-app-'));
    expect(hasAppIconSet(empty)).toBe(false);
    expect(() => appIcon(empty)).toThrow(/Contents\.json not found/);
    expect(() => appIcon(app([{ filename: 'gone.png', size: '1024x1024' }], []))).toThrow(
      /gone\.png not found/,
    );
    expect(() => appIcon(app([]))).toThrow(/lists no image/);
    expect(() => appIcon(app([{ filename: 'odd.png', size: 'large' }]))).toThrow(/no size/);
  });
});

describe('the icons in public/', () => {
  it.each([
    ['favicon-32.png', 32],
    ['apple-touch-icon.png', 180],
    ['icon-192.png', 192],
    ['icon-512.png', 512],
    ['icon-maskable-512.png', 512],
  ])('%s is %i px square and opaque, like the app’s icon', async (name, size) => {
    const { width, height, hasAlpha } = await sharp(join('public', name)).metadata();
    expect([width, height]).toEqual([size, size]);
    expect(hasAlpha).toBe(false);
  });

  it('has no SVG favicon: the source is a raster, and a favicon can’t load the font', () => {
    expect(existsSync('public/favicon.svg')).toBe(false);
  });
});

describe('config/app-icon.json', () => {
  const record = readRecord();

  it('names a PNG of the set, and the icons and the OG card come from the same file', () => {
    expect(record.path).toMatch(/^Ryft\/Assets\.xcassets\/AppIcon\.appiconset\/[^/]+\.png$/);
    expect(record.sha256).toMatch(/^[0-9a-f]{64}$/);
    expect(record.ogSha256, 'run `npm run og`').toBe(record.sha256);
  });

  // The drift check. Like tokens:check and the mark's drift test it needs the app (RYFT_APP_DIR,
  // ../../app by default): skipped without it, an error in CI, which checks the app out at the
  // commit in web/.app-ref. A pin bump that brings another icon fails here until `npm run icons`
  // and `npm run og` ran against that commit.
  const present = hasAppIconSet();
  if (!present && process.env.CI) {
    it('finds the app’s icon', () => {
      throw new Error(`${join(appDir(), APP_ICON_SET)} not found (set RYFT_APP_DIR).`);
    });
    return;
  }
  it.skipIf(!present)('matches the app’s largest icon', () => {
    const icon = appIcon();
    const fix = 'run `npm run icons` and `npm run og` with RYFT_APP_DIR at web/.app-ref';
    expect(record.path, `the app's largest icon is another file: ${fix}`).toBe(icon.path);
    expect(record.sha256, `the app's icon changed: ${fix}`).toBe(icon.sha256);
    expect(record.ogSha256, `the OG card has another icon: ${fix}`).toBe(icon.sha256);
  });
});

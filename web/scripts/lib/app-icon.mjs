// The app's icon (M1, web D22): the largest PNG of the app's AppIcon set, read where the app keeps
// it, never from a copy. scripts/icons.mjs and scripts/og.mjs draw from it, and
// tests/app-icon.test.ts compares it with config/app-icon.json (the drift check).
//
// The app is RYFT_APP_DIR (absolute, or relative to web/), ../../app by default, as in
// scripts/tokens.mjs. CI checks it out at the commit in web/.app-ref.
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const WEB = resolve(import.meta.dirname, '../..');

/** The set, relative to the app. */
export const APP_ICON_SET = 'Ryft/Assets.xcassets/AppIcon.appiconset';

/** What the committed icons and OG card were drawn from. */
export const RECORD_PATH = join(WEB, 'config/app-icon.json');

export function appDir(env = process.env) {
  return resolve(WEB, env.RYFT_APP_DIR ?? '../../app');
}

/** Whether the app's set is there at all (the test skips without it outside CI). */
export function hasAppIconSet(app = appDir()) {
  return existsSync(join(app, APP_ICON_SET, 'Contents.json'));
}

/**
 * The set's largest image: pixels = points × scale (no scale is 1x, as in a single-size iOS set).
 * On a tie, the entry without `appearances` (the universal one) wins. Throws when the set, a size
 * or the file is missing.
 */
export function appIcon(app = appDir()) {
  const contents = join(app, APP_ICON_SET, 'Contents.json');
  if (!existsSync(contents)) throw new Error(`app-icon: ${contents} not found (set RYFT_APP_DIR).`);
  const { images } = JSON.parse(readFileSync(contents, 'utf8'));
  const entries = (images ?? [])
    .filter((i) => i.filename)
    .map((i) => {
      const points = /^(\d+(?:\.\d+)?)x(\d+(?:\.\d+)?)$/.exec(i.size ?? '');
      const scale = i.scale ? Number.parseFloat(i.scale) : 1;
      if (!points || !(scale > 0)) {
        throw new Error(`app-icon: ${contents}: ${i.filename} has no size app-icon.mjs can read.`);
      }
      return {
        filename: i.filename,
        pixels: Math.round(Number(points[1]) * scale),
        universal: !i.appearances?.length,
      };
    })
    .sort((a, b) => b.pixels - a.pixels || Number(b.universal) - Number(a.universal));
  if (!entries.length) throw new Error(`app-icon: ${contents} lists no image.`);
  const [best] = entries;
  const path = `${APP_ICON_SET}/${best.filename}`;
  const file = join(app, path);
  if (!existsSync(file)) throw new Error(`app-icon: ${file} not found (Contents.json lists it).`);
  const bytes = readFileSync(file);
  return { path, file, bytes, pixels: best.pixels, sha256: sha256(bytes) };
}

export function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

/** config/app-icon.json: `path` and `sha256` from icons.mjs, `ogSha256` from og.mjs. */
export function readRecord() {
  return existsSync(RECORD_PATH) ? JSON.parse(readFileSync(RECORD_PATH, 'utf8')) : {};
}

export function writeRecord(fields) {
  const { path, sha256: hash, ogSha256 } = { ...readRecord(), ...fields };
  const record = {
    $comment:
      'The app icon (web D22) the committed icons (npm run icons: path, sha256) and the OG card (npm run og: ogSha256) were drawn from. tests/app-icon.test.ts fails when the app at web/.app-ref has another.',
    path,
    sha256: hash,
    ogSha256,
  };
  writeFileSync(RECORD_PATH, `${JSON.stringify(record, null, 2)}\n`);
}

/** Exit 1 with the message: the scripts fail loudly when the source is missing. */
export function appIconOrExit(tag) {
  try {
    return appIcon();
  } catch (error) {
    console.error(error.message.replace(/^app-icon:/, `${tag}:`));
    process.exit(1);
  }
}

#!/usr/bin/env node
// Real product screenshots from the app's captures (../app/docs/screenshots), in every
// size and format the pages use:
//   public/screenshots/<id>-<scheme>-<width>.{avif,webp}   three widths
//   public/screenshots/<id>-<scheme>.png                   one fallback
//   src/data/screenshots.json                              sizes for <Screenshot>
// Dark captures are shown in dark mode, light ones in light mode. The 1030 × 1500 Preview
// captures carry a canvas artefact top right, so they are cropped to the part a page shows.
// Output is committed: the build doesn't need ../app. RYFT_APP_DIR overrides ../app.
import { existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import sharp from 'sharp';

const ROOT = resolve(import.meta.dirname, '..');
const SOURCE = join(resolve(ROOT, process.env.RYFT_APP_DIR ?? '../app'), 'docs/screenshots');
const OUT = join(ROOT, 'public/screenshots');
const DATA = join(ROOT, 'src/data/screenshots.json');

/** id → source capture (without -dark/-light) and an optional vertical crop in source pixels. */
const SHOTS = [
  { id: 'sleep', source: 'sleep', kind: 'screen' },
  { id: 'strain', source: 'strain', kind: 'screen' },
  // The three insight cards, without status and tab bar, to sit next to the other crops.
  { id: 'insights-cards', source: 'insights', kind: 'crop', crop: { top: 492, bottom: 1904 } },
  // Rest timer and sets, below the session header (which carries the artefact).
  { id: 'train-session', source: 'train-session', kind: 'crop', crop: { top: 176, bottom: 1312 } },
  // Account card: change password, log out, delete account (no name or address in view).
  { id: 'profile-account', source: 'profile', kind: 'crop', crop: { top: 540, bottom: 962 } },
];
const WIDTHS = { screen: [360, 540, 720], crop: [400, 640, 960] };

if (!existsSync(SOURCE)) {
  console.error(`screenshots: ${SOURCE} not found (set RYFT_APP_DIR).`);
  process.exit(1);
}
rmSync(OUT, { recursive: true, force: true });
mkdirSync(OUT, { recursive: true });
mkdirSync(join(ROOT, 'src/data'), { recursive: true });

const data = {};
for (const shot of SHOTS) {
  const widths = WIDTHS[shot.kind];
  for (const scheme of ['dark', 'light']) {
    const file = join(SOURCE, `${shot.source}-${scheme}.png`);
    if (!existsSync(file)) throw new Error(`screenshots: ${file} is missing`);
    let image = sharp(file);
    const meta = await image.metadata();
    if (shot.crop) {
      image = image.extract({
        left: 0,
        top: shot.crop.top,
        width: meta.width,
        height: shot.crop.bottom - shot.crop.top,
      });
    }
    const base = await image.png().toBuffer();
    const { width, height } = await sharp(base).metadata();
    data[shot.id] = { kind: shot.kind, width, height, widths };
    for (const w of widths) {
      const resized = sharp(base).resize({ width: w });
      await resized
        .clone()
        .avif({ quality: 58, effort: 6 })
        .toFile(join(OUT, `${shot.id}-${scheme}-${w}.avif`));
      await resized
        .clone()
        .webp({ quality: 80, effort: 6 })
        .toFile(join(OUT, `${shot.id}-${scheme}-${w}.webp`));
    }
    await sharp(base)
      .resize({ width: widths[1] })
      .png({ compressionLevel: 9, palette: true, quality: 90 })
      .toFile(join(OUT, `${shot.id}-${scheme}.png`));
  }
}
writeFileSync(DATA, `${JSON.stringify(data, null, 2)}\n`);
const count = readdirSync(OUT).length;
console.log(`screenshots: ${SHOTS.length} captures × 2 appearances → ${count} files in public/screenshots/.`);

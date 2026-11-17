#!/usr/bin/env node
// The site's icons from the app's icon (M1, web D22): the largest PNG of the app's AppIcon set
// (scripts/lib/app-icon.mjs; 1024 × 1024 at the pin), scaled down with Lanczos. The app draws that
// raster itself (its scripts/icon-spec.swift and render-icon.swift: "RYFT" in Satoshi Light on the
// Background tile) and has no vector of it, so the raster is the source: the site doesn't redraw
// the word.
//   public/favicon-32.png         32 × 32
//   public/apple-touch-icon.png   180 × 180, full bleed (iOS rounds the corners)
//   public/icon-{192,512}.png     full bleed
//   public/icon-maskable-512.png  the word's ink inside the 80 % safe circle: the full-bleed icon
//                                 when it already is (measured and printed), padded with the tile
//                                 colour when it isn't
//   public/site.webmanifest       background and theme colour: the tokens' background
//   config/app-icon.json          the source's path and SHA-256 (tests/app-icon.test.ts, the drift check)
// No favicon.svg: a raster gives no vector without converting the font (ITF FFL), and a favicon
// can't load a web font. The output is committed. Run again, with RYFT_APP_DIR at the app's commit
// in web/.app-ref, when the app's icon or the tokens change; then `npm run og`.
import { readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import sharp from 'sharp';
import { appIconOrExit, writeRecord } from './lib/app-icon.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const PUBLIC = join(ROOT, 'public');
const tokens = JSON.parse(readFileSync(join(ROOT, 'src/styles/tokens.json'), 'utf8'));
const background = tokens.colors.background.dark;

// The maskable safe zone: a circle of 80 % of the icon's side, centred (radius 0.4 of the side).
const SAFE_RADIUS = 0.4;

const icon = appIconOrExit('icons');
const meta = await sharp(icon.bytes).metadata();
const { isOpaque } = await sharp(icon.bytes).stats();
const side = meta.width;
if (meta.width !== meta.height || side !== icon.pixels || !isOpaque) {
  fail(
    `${icon.path} is ${meta.width} × ${meta.height}${isOpaque ? '' : ', not opaque'}; Contents.json says ${icon.pixels} × ${icon.pixels}, opaque.`,
  );
}
console.log(`icons: source ${icon.path} (${side} × ${side}, sha256 ${icon.sha256.slice(0, 12)}…).`);

// The word's ink: every pixel that differs from the tile (the top-left pixel), as a box in pixel
// edges (x0..x1 wide), so its corners are where the ink's outermost pixels end.
const { data, info } = await sharp(icon.bytes).removeAlpha().raw().toBuffer({ resolveWithObject: true });
const tile = [data[0], data[1], data[2]];
let [x0, y0, x1, y1] = [side, side, 0, 0];
for (let y = 0; y < side; y++) {
  for (let x = 0; x < side; x++) {
    const i = (y * side + x) * info.channels;
    if (data[i] !== tile[0] || data[i + 1] !== tile[1] || data[i + 2] !== tile[2]) {
      x0 = Math.min(x0, x);
      y0 = Math.min(y0, y);
      x1 = Math.max(x1, x + 1);
      y1 = Math.max(y1, y + 1);
    }
  }
}
if (x1 <= x0 || x0 === 0 || y0 === 0 || x1 === side || y1 === side) {
  fail(`no ink, or ink at the tile's edge (${x0}..${x1} × ${y0}..${y1}): the tile isn't one colour.`);
}
const hex = `#${tile.map((v) => v.toString(16).padStart(2, '0')).join('')}`;
const centre = side / 2;
const corners = [x0, x1].flatMap((x) => [y0, y1].map((y) => Math.hypot(x - centre, y - centre)));
const farthest = Math.max(...corners);
const safe = SAFE_RADIUS * side;
console.log(
  `icons: tile ${hex} (the tokens' background ${background}); ink x ${x0}..${x1}, y ${y0}..${y1} (${x1 - x0} × ${y1 - y0} px).`,
);
console.log(
  `icons: maskable: the ink's farthest corner is ${farthest.toFixed(1)} px from the centre (${((farthest / side) * 100).toFixed(1)} % of the side); the safe circle's radius is ${safe.toFixed(1)} px (40 %).`,
);

// Inside: the maskable icon is the full-bleed one. Outside: a bigger tile of the same colour, the
// same padding on every side (so the centre doesn't move), until the farthest corner is inside.
let maskable = icon.bytes;
if (farthest > safe) {
  const pad = Math.ceil((farthest / SAFE_RADIUS - side) / 2);
  maskable = await sharp(icon.bytes)
    .extend({ top: pad, bottom: pad, left: pad, right: pad, background: hex })
    .png()
    .toBuffer();
  const padded = side + 2 * pad;
  console.log(`icons: maskable: outside, padded to ${padded} × ${padded} with ${hex} before scaling.`);
} else {
  console.log(
    `icons: maskable: inside by ${(safe - farthest).toFixed(1)} px: the full-bleed icon, no padding.`,
  );
}

const png = (input, size) =>
  sharp(input)
    .resize(size, size, { kernel: sharp.kernel.lanczos3 })
    .removeAlpha()
    .png({ compressionLevel: 9 })
    .toBuffer();

const outputs = [
  ['favicon-32.png', icon.bytes, 32],
  ['apple-touch-icon.png', icon.bytes, 180],
  ['icon-192.png', icon.bytes, 192],
  ['icon-512.png', icon.bytes, 512],
  ['icon-maskable-512.png', maskable, 512],
];
for (const [name, input, size] of outputs) writeFileSync(join(PUBLIC, name), await png(input, size));

const manifest = {
  name: 'RYFT',
  short_name: 'RYFT',
  description: 'Your whole health.',
  start_url: '/',
  scope: '/',
  display: 'browser',
  background_color: background,
  theme_color: background,
  icons: [
    { src: '/icon-192.png', sizes: '192x192', type: 'image/png' },
    { src: '/icon-512.png', sizes: '512x512', type: 'image/png' },
    { src: '/icon-maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
  ],
};
writeFileSync(join(PUBLIC, 'site.webmanifest'), `${JSON.stringify(manifest, null, 2)}\n`);
writeRecord({ path: icon.path, sha256: icon.sha256 });
console.log(
  `icons: ${outputs.map(([name]) => name).join(', ')}, site.webmanifest in public/; config/app-icon.json.`,
);

function fail(message) {
  console.error(`icons: ${message}`);
  process.exit(1);
}

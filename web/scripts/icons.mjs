#!/usr/bin/env node
// Icons from the mark's geometry (src/lib/mark.ts, the port of RyftMark.swift) in the
// token colours: like the app icon (dark), a Mist mark on Graphite.
//   public/favicon.svg            mark only; Graphite on light tabs, Mist on dark tabs
//   public/favicon-32.png         the same on Graphite, for browsers without SVG icons
//   public/apple-touch-icon.png   180 × 180, full bleed (iOS rounds the corners)
//   public/icon-{192,512}.png, public/icon-maskable-512.png, public/site.webmanifest
// The output is committed; run again when the mark or the tokens change.
import { readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import sharp from 'sharp';
import { markSvg, OUTER_SPAN } from '../src/lib/mark.ts';

const ROOT = resolve(import.meta.dirname, '..');
const PUBLIC = join(ROOT, 'public');
const tokens = JSON.parse(readFileSync(join(ROOT, 'src/styles/tokens.json'), 'utf8'));
const graphite = tokens.colors.graphite.dark;
const mist = tokens.colors.mist.dark;

// The app icon's composition (icon-spec.swift): the outer ring's span is 0.963 of the tile,
// so the tile is OUTER_SPAN / 0.963 box units wide, centred on the mark (negative padding).
const RING_SPAN = 0.963;
const PADDING = (OUTER_SPAN / RING_SPAN - 100) / 2;
// Maskable: the whole mark, dots included (they reach 50 from the centre), inside the
// 80 % safe circle: 2 × 50 box units = 0.8 of the tile, i.e. padding 12.5.
const MASKABLE_PADDING = 12.5;
const maskableScale = (100 + 2 * PADDING) / (100 + 2 * MASKABLE_PADDING);

// The favicon adapts to the tab: its own <style>, colours from the tokens. One SVG serves
// 16 and 32 px, so the small-size rule is applied for 24 px (stroke 1.25 px there).
const favicon = markSvg({ size: 24, padding: PADDING })
  .replace('<svg ', '<svg class="m" ')
  .replace(
    /^(<svg[^>]*>)/,
    `$1<style>.m{color:${graphite}}@media (prefers-color-scheme:dark){.m{color:${mist}}}</style>`,
  );
writeFileSync(join(PUBLIC, 'favicon.svg'), `${favicon}\n`);

const png = (size, padding) =>
  sharp(Buffer.from(markSvg({ size, padding, color: mist, background: graphite })))
    .png({ compressionLevel: 9 })
    .toBuffer();

const outputs = [
  ['favicon-32.png', 32, PADDING],
  ['apple-touch-icon.png', 180, PADDING],
  ['icon-192.png', 192, PADDING],
  ['icon-512.png', 512, PADDING],
  // Maskable: the whole mark inside the 80 % safe circle.
  ['icon-maskable-512.png', 512, MASKABLE_PADDING],
];
for (const [name, size, padding] of outputs) writeFileSync(join(PUBLIC, name), await png(size, padding));

const manifest = {
  name: 'RYFT',
  short_name: 'RYFT',
  description: 'Your whole health.',
  start_url: '/',
  scope: '/',
  display: 'browser',
  background_color: graphite,
  theme_color: graphite,
  icons: [
    { src: '/icon-192.png', sizes: '192x192', type: 'image/png' },
    { src: '/icon-512.png', sizes: '512x512', type: 'image/png' },
    { src: '/icon-maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
  ],
};
writeFileSync(join(PUBLIC, 'site.webmanifest'), `${JSON.stringify(manifest, null, 2)}\n`);
console.log(`icons: favicon, apple-touch-icon, ${outputs.length} PNGs and site.webmanifest in public/.`);
console.log(`icons: maskable scale ${maskableScale.toFixed(3)} of the reference composition.`);

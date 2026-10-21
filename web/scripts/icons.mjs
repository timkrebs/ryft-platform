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
import { markSvg } from '../src/lib/mark.ts';

const ROOT = resolve(import.meta.dirname, '..');
const PUBLIC = join(ROOT, 'public');
const tokens = JSON.parse(readFileSync(join(ROOT, 'src/styles/tokens.json'), 'utf8'));
const graphite = tokens.colors.graphite.dark;
const mist = tokens.colors.mist.dark;

// The favicon adapts to the tab: its own <style>, colours from the tokens.
const favicon = markSvg({ size: 32, padding: 2 })
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
  ['favicon-32.png', 32, 8],
  ['apple-touch-icon.png', 180, 24],
  ['icon-192.png', 192, 24],
  ['icon-512.png', 512, 24],
  // Maskable: the mark inside the 80 % safe zone.
  ['icon-maskable-512.png', 512, 40],
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

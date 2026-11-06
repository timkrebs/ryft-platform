#!/usr/bin/env node
// Screenshots for docs/STATUS.md from the running image (npm run image && npm run serve):
// every page's first screen, the home page on a phone and in full, and the app-vs-web
// comparison (the app's light capture next to the site; the site is light only, web D19).
// JPEG to keep the repository small. BASE_URL overrides :8080.
import { mkdirSync, rmSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { chromium } from '@playwright/test';
import sharp from 'sharp';

const ROOT = resolve(import.meta.dirname, '..');
const OUT = join(ROOT, 'docs/screenshots');
const APP = resolve(ROOT, process.env.RYFT_APP_DIR ?? '../../app');
const BASE = process.env.BASE_URL ?? 'http://localhost:8080';
const PAGES = [
  '/',
  '/science',
  '/beta',
  '/privacy',
  '/terms',
  '/impressum',
  '/support',
  '/licenses',
  '/styleguide',
  '/open',
  '/auth/callback',
  '/nope',
];

rmSync(OUT, { recursive: true, force: true });
mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const name = (p) => (p === '/' ? 'home' : p.slice(1).replace(/\//g, '-'));

async function shoot(path, file, viewport = { width: 1280, height: 900 }, fullPage = false) {
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 1, reducedMotion: 'reduce' });
  const page = await ctx.newPage();
  await page.goto(BASE + path, { waitUntil: 'networkidle' });
  await page.screenshot({ path: join(OUT, file), type: 'jpeg', quality: 78, fullPage });
  await ctx.close();
}

for (const p of PAGES) await shoot(p, `${name(p)}.jpg`);
await shoot('/', 'home-phone.jpg', { width: 390, height: 844 });
await shoot('/', 'home-full.jpg', { width: 1280, height: 900 }, true);

// The app's light capture (left) next to the site (right).
{
  const height = 900;
  const app = await sharp(join(APP, 'docs/screenshots/strain-light.png')).resize({ height }).toBuffer();
  const appWidth = (await sharp(app).metadata()).width;
  await sharp({ create: { width: appWidth + 1280 + 40, height, channels: 3, background: '#fff' } })
    .composite([
      { input: app, left: 0, top: 0 },
      { input: join(OUT, 'home.jpg'), left: appWidth + 40, top: 0 },
    ])
    .jpeg({ quality: 78 })
    .toFile(join(OUT, 'compare.jpg'));
}
await browser.close();
console.log(`status shots: docs/screenshots/ (${PAGES.length + 3} images).`);

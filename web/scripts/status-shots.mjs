#!/usr/bin/env node
// Screenshots for docs/STATUS.md from the running image (npm run image && npm run serve):
// every page's first screen in dark and light (English), the German home page, the home
// page on a phone, and the app-vs-web comparisons (the app's capture next to the site in
// the same appearance). JPEG to keep the repository small. BASE_URL overrides :8080.
import { mkdirSync, rmSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { chromium } from '@playwright/test';
import sharp from 'sharp';

const ROOT = resolve(import.meta.dirname, '..');
const OUT = join(ROOT, 'docs/screenshots');
const APP = resolve(ROOT, process.env.RYFT_APP_DIR ?? '../app');
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

async function shoot(path, scheme, file, viewport = { width: 1280, height: 900 }, fullPage = false) {
  const ctx = await browser.newContext({
    viewport,
    colorScheme: scheme,
    deviceScaleFactor: 1,
    reducedMotion: 'reduce',
  });
  const page = await ctx.newPage();
  await page.goto(BASE + path, { waitUntil: 'networkidle' });
  await page.screenshot({ path: join(OUT, file), type: 'jpeg', quality: 78, fullPage });
  await ctx.close();
}

for (const p of PAGES) for (const s of ['dark', 'light']) await shoot(p, s, `${name(p)}-${s}.jpg`);
for (const s of ['dark', 'light']) {
  await shoot('/de', s, `de-home-${s}.jpg`);
  await shoot('/', s, `home-phone-${s}.jpg`, { width: 390, height: 844 });
  await shoot('/', s, `home-full-${s}.jpg`, { width: 1280, height: 900 }, true);
}

// The app's capture (left) next to the site in the same appearance (right).
for (const s of ['dark', 'light']) {
  const height = 900;
  const app = await sharp(join(APP, `docs/screenshots/strain-${s}.png`))
    .resize({ height })
    .toBuffer();
  const appWidth = (await sharp(app).metadata()).width;
  const web = join(OUT, `home-${s}.jpg`);
  await sharp({
    create: { width: appWidth + 1280 + 40, height, channels: 3, background: s === 'dark' ? '#000' : '#fff' },
  })
    .composite([
      { input: app, left: 0, top: 0 },
      { input: web, left: appWidth + 40, top: 0 },
    ])
    .jpeg({ quality: 78 })
    .toFile(join(OUT, `compare-${s}.jpg`));
}
await browser.close();
console.log(`status shots: docs/screenshots/ (${PAGES.length * 2 + 8} images).`);

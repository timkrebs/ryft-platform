#!/usr/bin/env node
// Open Graph / Twitter images, one per locale: public/og/<locale>.png, 1200 × 630.
// Rendered in Chromium (Playwright) from an HTML card that uses the self-hosted Satoshi
// files and the token colours, so the image is ordinary use of the font (ITF FFL), not
// a converted copy of it. The output is committed; run again when the copy or the tokens
// change. Needs `npm run fonts` and `npx playwright install chromium`.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { chromium } from '@playwright/test';
import { markSvg } from '../src/lib/mark.ts';

const ROOT = resolve(import.meta.dirname, '..');
const tokens = JSON.parse(readFileSync(join(ROOT, 'src/styles/tokens.json'), 'utf8'));
const c = (name) => tokens.colors[name].dark;
const font = (w) => pathToFileURL(join(ROOT, `src/fonts/Satoshi-${w}.woff2`)).href;
const CACHE = join(ROOT, '.cache/og');
mkdirSync(CACHE, { recursive: true });
mkdirSync(join(ROOT, 'public/og'), { recursive: true });

const copy = {
  en: {
    tagline: 'Your whole health.',
    line: 'Readiness · Recovery · Strain · Sleep',
    foot: 'iPhone · Apple Health · Hosted in the EU',
  },
  de: {
    tagline: 'Deine ganze Gesundheit.',
    line: 'Bereitschaft · Erholung · Belastung · Schlaf',
    foot: 'iPhone · Apple Health · Gehostet in der EU',
  },
};

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1 });
for (const [locale, t] of Object.entries(copy)) {
  const html = `<!doctype html><html lang="${locale}"><head><meta charset="utf-8"><style>
@font-face{font-family:Satoshi;font-weight:400;src:url(${font('Regular')}) format('woff2')}
@font-face{font-family:Satoshi;font-weight:500;src:url(${font('Medium')}) format('woff2')}
*{box-sizing:border-box;margin:0}
body{width:1200px;height:630px;background:${c('graphite')};color:${c('mist')};font-family:Satoshi;overflow:hidden;position:relative}
.card{position:absolute;inset:72px 80px;display:flex;flex-direction:column;justify-content:space-between}
.logo{display:flex;align-items:center;gap:20px;font-weight:500;font-size:34px;letter-spacing:.32em}
h1{font-weight:500;font-size:84px;line-height:1.05;letter-spacing:-.02em;max-width:760px}
.line{margin-top:22px;font-size:28px;color:${c('teal')}}
.foot{font-size:22px;letter-spacing:.18em;text-transform:uppercase;color:${c('text-secondary')}}
.big{position:absolute;right:-120px;top:50%;transform:translateY(-50%);width:560px;height:560px;color:${c('slate')}}
.rule{position:absolute;left:80px;right:80px;bottom:132px;height:1px;background:${c('separator')}}
</style></head><body>
<div class="big">${markSvg({ size: 560 })}</div>
<div class="rule"></div>
<div class="card">
  <div class="logo">${markSvg({ size: 52 })}<span>RYFT</span></div>
  <div><h1>${t.tagline}</h1><p class="line">${t.line}</p></div>
  <p class="foot">${t.foot}</p>
</div></body></html>`;
  const file = join(CACHE, `${locale}.html`);
  writeFileSync(file, html);
  await page.goto(pathToFileURL(file).href);
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: join(ROOT, 'public/og', `${locale}.png`) });
}
await browser.close();
console.log('og: public/og/en.png and public/og/de.png (1200 × 630).');

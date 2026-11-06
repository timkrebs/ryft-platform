#!/usr/bin/env node
// The Open Graph / Twitter image: public/og/en.png, 1200 × 630.
// Rendered in Chromium (Playwright) from an HTML card that uses the committed Inter files
// and the light token colours, in the site's typographic design (web D19–D21). The output is committed; run again when the copy or the tokens
// change. Needs `npx playwright install chromium`.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { chromium } from '@playwright/test';
import { markSvg } from '../src/lib/mark.ts';

const ROOT = resolve(import.meta.dirname, '..');
const tokens = JSON.parse(readFileSync(join(ROOT, 'src/styles/tokens.json'), 'utf8'));
// The site is light only (web D19): the card is ink on paper, like the hero.
const c = (name) => tokens.colors[name].light;
// The committed Inter subsets (web D20), read from disk: offline, no CDN.
const font = (w) => pathToFileURL(join(ROOT, `src/fonts/Inter-${w}.woff2`)).href;
const en = JSON.parse(readFileSync(join(ROOT, 'messages/en.json'), 'utf8'));
const CACHE = join(ROOT, '.cache/og');
mkdirSync(CACHE, { recursive: true });
mkdirSync(join(ROOT, 'public/og'), { recursive: true });

// The hero's words (messages/en.json), so the card never says something the page does not.
const copy = {
  en: {
    title: en.home.hero.title,
    left: en.home.hero.metaLeft,
    right: 'Beta · Hosted in the EU',
    line: 'Readiness · Recovery · Strain · Sleep',
    tagline: en.site.tagline,
  },
};
const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1 });
for (const [locale, t] of Object.entries(copy)) {
  const html = `<!doctype html><html lang="${locale}"><head><meta charset="utf-8"><style>
@font-face{font-family:Inter;font-weight:400;src:url(${font('Regular')}) format('woff2')}
@font-face{font-family:Inter;font-weight:700;src:url(${font('Bold')}) format('woff2')}
*{box-sizing:border-box;margin:0}
body{width:1200px;height:630px;background:${c('background')};color:${c('text-primary')};font-family:Inter;font-synthesis:none;overflow:hidden;position:relative}
.guides{position:absolute;inset:0;display:grid;grid-template-columns:repeat(5,1fr)}
.guides span{border-left:1px solid color-mix(in srgb,${c('separator')} 14%,transparent)}
.guides span:first-child{border-left:0}
.card{position:absolute;inset:48px 64px;display:flex;flex-direction:column;justify-content:space-between}
.label{font-size:20px;letter-spacing:.08em;text-transform:uppercase}
.top{display:flex;align-items:center;justify-content:space-between;padding-bottom:20px;border-bottom:1px solid ${c('separator')}}
.logo{display:flex;align-items:center;gap:16px;font-weight:700;font-size:34px;letter-spacing:-.02em}
h1{font-weight:700;font-size:150px;line-height:.8;letter-spacing:-.07em;text-transform:uppercase}
h1 span{display:block}
.bottom{display:flex;justify-content:space-between;padding-top:20px;border-top:1px solid ${c('separator')}}
.secondary{color:${c('text-secondary')}}
</style></head><body>
<div class="guides"><span></span><span></span><span></span><span></span><span></span></div>
<div class="card">
  <div class="top"><div class="logo">${markSvg({ size: 44 })}<span>RYFT</span></div><p class="label">${esc(t.left)}</p></div>
  <h1>${t.title.map((l) => `<span>${esc(l)}</span>`).join('')}</h1>
  <div class="bottom"><p class="label">${esc(t.line)}</p><p class="label secondary">${esc(t.right)}</p></div>
</div></body></html>`;
  const file = join(CACHE, `${locale}.html`);
  writeFileSync(file, html);
  await page.goto(pathToFileURL(file).href);
  await page.evaluate(() => document.fonts.ready);
  // Every face the card uses must be the committed Inter, not a system fallback.
  const loaded = await page.evaluate(() => [...document.fonts].filter((f) => f.status === 'loaded').length);
  if (loaded !== 2) throw new Error(`og: expected both Inter faces loaded, got ${loaded}`);
  await page.screenshot({ path: join(ROOT, 'public/og', `${locale}.png`) });
}
await browser.close();
console.log('og: public/og/en.png (1200 × 630).');

#!/usr/bin/env node
// The Open Graph / Twitter image: public/og/en.png, 1200 × 630.
// Rendered in Chromium (Playwright) from an HTML card that uses the self-hosted Satoshi files, the
// tokens (colours, the title's weight, the label's case and tracking) and the app's icon (M1, web
// D22): the same source as scripts/icons.mjs (scripts/lib/app-icon.mjs), scaled by sharp (Lanczos)
// and inlined as data URLs. The icon is a rounded tile in the logo row (it says RYFT, so the row
// has no text of its own) and a big tile, uncropped, on the right. The tile is the card's own
// colour, so a 1 px separator border outlines it. The text is ordinary use of the font (ITF FFL),
// not a converted copy of it. The output is committed; run it again, with RYFT_APP_DIR at the
// app's commit in web/.app-ref, when the copy, the tokens or the app's icon change (it records the
// icon in config/app-icon.json). Needs `npm run fonts` and `npx playwright install chromium`.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { chromium } from '@playwright/test';
import sharp from 'sharp';
import { appIconOrExit, writeRecord } from './lib/app-icon.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const tokens = JSON.parse(readFileSync(join(ROOT, 'src/styles/tokens.json'), 'utf8'));
const c = (name) => tokens.colors[name].dark;
const { scale, labelTrackingEm, labelTransform } = tokens.type;
const font = (w) => pathToFileURL(join(ROOT, `src/fonts/Satoshi-${w}.woff2`)).href;
// Every weight in src/fonts/ (config/fonts.json); Chromium loads only those the card uses.
const FACES = { 300: 'Light', 400: 'Regular', 500: 'Medium', 700: 'Bold' };
const face = ([w, name]) =>
  `@font-face{font-family:Satoshi;font-weight:${w};src:url(${font(name)}) format('woff2')}`;
const faces = Object.entries(FACES).map(face).join('\n');
const CACHE = join(ROOT, '.cache/og');
mkdirSync(CACHE, { recursive: true });
mkdirSync(join(ROOT, 'public/og'), { recursive: true });

// The card, and the icon's two sizes in it (px; the border adds 1 on each side). LOGO is the old
// mark's size in the logo row; BIG fits between the card's top and the rule, right of the copy.
const [WIDTH, HEIGHT] = [1200, 630];
const LOGO = 52;
const BIG = 300;
// The iOS-like corner: 22.37 % of the side (CSS draws it circular, not continuous).
const CORNER = 0.2237;
// The copy and the big tile keep at least this much space between them, and the tile stays above
// the rule; og.mjs measures both in the page and fails otherwise.
const MIN_GAP = 48;

const icon = appIconOrExit('og');
const tile = async (size) => {
  const png = await sharp(icon.bytes)
    .resize(size, size, { kernel: sharp.kernel.lanczos3 })
    .removeAlpha()
    .png()
    .toBuffer();
  return `data:image/png;base64,${png.toString('base64')}`;
};
const [logo, big] = await Promise.all([tile(LOGO), tile(BIG)]);
// The border's outer radius, so its inner edge has the icon's corner.
const radius = (size) => +(CORNER * size + 1).toFixed(2);

const copy = {
  en: {
    tagline: 'Your whole health.',
    line: 'Readiness · Recovery · Strain · Sleep',
    foot: 'iPhone · Apple Health · Hosted in the EU',
  },
};

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: WIDTH, height: HEIGHT }, deviceScaleFactor: 1 });
for (const [locale, t] of Object.entries(copy)) {
  const html = `<!doctype html><html lang="${locale}"><head><meta charset="utf-8"><style>
${faces}
*{box-sizing:border-box;margin:0}
body{width:${WIDTH}px;height:${HEIGHT}px;background:${c('graphite')};color:${c('mist')};font-family:Satoshi;font-weight:${scale.body.weight};overflow:hidden;position:relative}
.card{position:absolute;inset:72px 80px;display:flex;flex-direction:column;justify-content:space-between}
.logo{display:flex;align-items:center}
.tile{display:block;box-sizing:content-box;border:1px solid ${c('separator')}}
.logo .tile{width:${LOGO}px;height:${LOGO}px;border-radius:${radius(LOGO)}px}
h1{font-weight:${scale.title.weight};font-size:84px;line-height:1.05;letter-spacing:-.02em;max-width:760px}
.line{margin-top:22px;font-size:28px;color:${c('teal')}}
.foot{font-size:22px;letter-spacing:${labelTrackingEm}em;text-transform:${labelTransform};color:${c('text-secondary')}}
.big{position:absolute;right:80px;top:${(HEIGHT - BIG - 2) / 2}px;width:${BIG}px;height:${BIG}px;border-radius:${radius(BIG)}px}
.rule{position:absolute;left:80px;right:80px;bottom:132px;height:1px;background:${c('separator')}}
</style></head><body>
<img class="tile big" src="${big}" alt="">
<div class="rule"></div>
<div class="card">
  <div class="logo"><img class="tile" src="${logo}" alt="RYFT"></div>
  <div><h1>${t.tagline}</h1><p class="line">${t.line}</p></div>
  <p class="foot">${t.foot}</p>
</div></body></html>`;
  const file = join(CACHE, `${locale}.html`);
  writeFileSync(file, html);
  await page.goto(pathToFileURL(file).href);
  await page.evaluate(() => document.fonts.ready);
  // The copy's ink (not its boxes: the h1 is up to 760 px wide) against the big tile and the rule.
  const layout = await page.evaluate(() => {
    const ink = (el) => {
      const range = document.createRange();
      range.selectNodeContents(el);
      return range.getBoundingClientRect();
    };
    const text = [...document.querySelectorAll('h1, .line')].map(ink);
    const tileBox = document.querySelector('.big').getBoundingClientRect();
    const rule = document.querySelector('.rule').getBoundingClientRect();
    return {
      gap: tileBox.left - Math.max(...text.map((r) => r.right)),
      above: rule.top - tileBox.bottom,
      tile: [tileBox.left, tileBox.top, tileBox.right, tileBox.bottom],
    };
  });
  console.log(
    `og: ${locale}: big tile at x ${layout.tile[0]}..${layout.tile[2]}, y ${layout.tile[1]}..${layout.tile[3]}; ${layout.gap.toFixed(1)} px right of the copy, ${layout.above.toFixed(1)} px above the rule.`,
  );
  if (layout.gap < MIN_GAP || layout.above < 0) {
    await browser.close();
    console.error(`og: ${locale}: the big tile collides with the copy or the rule (BIG = ${BIG}).`);
    process.exit(1);
  }
  await page.screenshot({ path: join(ROOT, 'public/og', `${locale}.png`) });
}
await browser.close();
writeRecord({ ogSha256: icon.sha256 });
console.log(`og: public/og/en.png (${WIDTH} × ${HEIGHT}) from ${icon.path}; config/app-icon.json.`);

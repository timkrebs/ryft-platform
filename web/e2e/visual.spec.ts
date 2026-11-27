import { mkdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { expect, test } from '@playwright/test';

// Full-page screenshots for review: every page of the site under a dark and a light system setting, on
// both projects (desktop and phone). The site is dark-only (web D20), so the two settings must look the
// same. Where they go: `<page>-<dark|light>-<desktop|phone>.jpg` in RYFT_SHOTS_DIR when it is set (the
// PR's review images), else test-results/ (CI uploads that folder in the artifact `playwright-report`).
// site.spec.ts attaches every page to the HTML report as well.
//
// Personal details are masked before the image is taken (Playwright's `mask`, so the pixels never
// exist): the operator's name, address and phone sit in the Impressum, in the first section of the
// terms and in the controller line of the privacy policies. Whatever the text, a paragraph or list item
// with a postcode, a phone number or the controller / provider sentence is covered, and on the
// Impressum every paragraph is.
const SHOTS: [name: string, path: string, status: number][] = [
  ['home', '/', 200],
  ['science', '/science', 200],
  ['beta', '/beta', 200],
  ['support', '/support', 200],
  ['privacy', '/privacy', 200],
  ['privacy-2026-10-20', '/privacy/2026-10-20', 200],
  ['terms', '/terms', 200],
  ['impressum', '/impressum', 200],
  ['licenses', '/licenses', 200],
  ['open', '/open', 200],
  ['404', '/no-such-page', 404],
  ['styleguide', '/styleguide', 200],
];

const PERSONAL = /\b\d{5}\b|\+\d{2}[\d ]{6,}|Controller:|is provided by/;
// The mask's fill is a token (surface-raised), so a masked block reads as a block, not as an error.
const tokens = JSON.parse(readFileSync(join(process.cwd(), 'src', 'styles', 'tokens.json'), 'utf8'));
const MASK_COLOR: string = tokens.colors['surface-raised'].dark;

for (const [name, path, status] of SHOTS) {
  for (const scheme of ['dark', 'light'] as const) {
    test(`screenshot: ${name} (${scheme})`, async ({ page }, info) => {
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });
      const res = await page.goto(path, { waitUntil: 'networkidle' });
      expect(res?.status()).toBe(status);
      // Lazy images load only near the viewport, and a full-page capture doesn't scroll: load them
      // all now and wait until each is decoded (`complete` alone doesn't mean it was painted).
      await page.evaluate(async () => {
        const images = Array.from(document.images);
        for (const img of images) img.loading = 'eager';
        await Promise.all(images.map((img) => img.decode().catch(() => undefined)));
      });
      const painted = () =>
        page.evaluate(() => Array.from(document.images).every((i) => i.complete && i.naturalWidth > 0));
      await expect.poll(painted).toBe(true);
      await page.evaluate(() => document.fonts.ready.then(() => true));

      const prose = page.locator('main p, main li');
      const personal = path === '/impressum' ? prose : prose.filter({ hasText: PERSONAL });
      if (/^\/(terms|privacy|impressum)/.test(path)) {
        // Nothing that matches sits outside a paragraph or a list item (a heading, a table cell), where
        // the mask would miss it. (Whether a page has details to mask depends on its text: no count.)
        const elsewhere = page.locator('main :is(h1, h2, h3, h4, td, th, dt, dd, blockquote, pre)');
        const leaked = await elsewhere.filter({ hasText: PERSONAL }).count();
        expect(leaked, `${path}: nothing else matches`).toBe(0);
      }
      const file = `${name}-${scheme}-${info.project.name}.jpg`;
      const target = process.env.RYFT_SHOTS_DIR
        ? join(process.env.RYFT_SHOTS_DIR, file)
        : info.outputPath(file);
      mkdirSync(dirname(target), { recursive: true });
      await page.screenshot({
        path: target,
        fullPage: true,
        type: 'jpeg',
        quality: 72,
        scale: 'css',
        mask: [personal, page.locator('a[href^="tel:"]')],
        maskColor: MASK_COLOR,
      });

      // The home page is long: its first screen and its sections close up, so the instruments can be read.
      if (name === 'home' && scheme === 'dark') {
        const parts: [string, string][] = [
          ['hero', 'main > :first-child'],
          ['scores', '#scores'],
          ['features', '#features'],
        ];
        for (const [part, selector] of parts) {
          const detail = `home-${part}-${scheme}-${info.project.name}.jpg`;
          const to = process.env.RYFT_SHOTS_DIR
            ? join(process.env.RYFT_SHOTS_DIR, detail)
            : info.outputPath(detail);
          await page.locator(selector).screenshot({ path: to, type: 'jpeg', quality: 80, scale: 'css' });
        }
      }
    });
  }
}

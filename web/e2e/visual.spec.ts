import { expect, test } from '@playwright/test';

// Full-page screenshots for review, as files in test-results/ (CI uploads that folder in the
// artifact `playwright-report`): the home page, the privacy policy, support and the 404 page, under
// a dark and a light system setting, on both projects. The site is dark-only (web D20), so the two
// settings must look the same. site.spec.ts attaches every page to the HTML report as well.
const SHOTS: [name: string, path: string, status: number][] = [
  ['home', '/', 200],
  ['privacy', '/privacy', 200],
  ['support', '/support', 200],
  ['404', '/no-such-page', 404],
];

for (const [name, path, status] of SHOTS) {
  for (const scheme of ['dark', 'light'] as const) {
    test(`screenshot: ${name} (${scheme})`, async ({ page }, info) => {
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });
      const res = await page.goto(path, { waitUntil: 'networkidle' });
      expect(res?.status()).toBe(status);
      await page.evaluate(() => document.fonts.ready.then(() => true));
      await page.screenshot({ path: info.outputPath(`${name}-${scheme}.png`), fullPage: true });
    });
  }
}

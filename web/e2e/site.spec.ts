import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { ALL } from './pages';

// Every page in both languages, dark and light: renders, no console errors (CSP
// violations show up there), no serious or critical axe findings, a screenshot each.
for (const path of ALL) {
  for (const scheme of ['dark', 'light'] as const) {
    test(`${path} (${scheme})`, async ({ page }, info) => {
      const errors: string[] = [];
      page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
      page.on('pageerror', (e) => errors.push(String(e)));
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: 'reduce' });

      const res = await page.goto(path, { waitUntil: 'networkidle' });
      expect(res?.status()).toBe(200);
      await expect(page.locator('h1').first()).toBeVisible();
      expect(await page.locator('html').getAttribute('lang')).toBe(path.startsWith('/de') ? 'de' : 'en');
      expect(await page.title()).toMatch(/RYFT/);

      const axe = await new AxeBuilder({ page })
        .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
        .analyze();
      const serious = axe.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
      expect(serious.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([]);
      expect(errors).toEqual([]);

      await info.attach(`${path}-${scheme}`, {
        body: await page.screenshot({ fullPage: true }),
        contentType: 'image/png',
      });
    });
  }
}

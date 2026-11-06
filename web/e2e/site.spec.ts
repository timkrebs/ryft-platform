import AxeBuilder from '@axe-core/playwright';
import { expect, test } from '@playwright/test';
import { ALL } from './pages';

// Every page: renders, no console errors (CSP violations show up there), no serious or
// critical axe findings, a screenshot each. The site is light only (web D19).
//
// axe runs in a second context that bypasses the CSP, only so that one <style> can switch
// `content-visibility: auto` off: below-the-fold sections are skipped by the renderer until
// they come near the viewport, and axe would otherwise judge their colours unrendered (a
// flaky, partly blind result). The CSP itself is checked in the first context, unchanged.
const UNSKIP = '*{content-visibility:visible!important}';

for (const path of ALL) {
  test(path, async ({ page, browser }, info) => {
    const errors: string[] = [];
    page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
    page.on('pageerror', (e) => errors.push(String(e)));
    await page.emulateMedia({ reducedMotion: 'reduce' });

    const res = await page.goto(path, { waitUntil: 'networkidle' });
    expect(res?.status()).toBe(200);
    await expect(page.locator('h1').first()).toBeVisible();
    expect(await page.locator('html').getAttribute('lang')).toBe('en');
    expect(await page.title()).toMatch(/RYFT/);
    // Only Regular and Bold exist; nothing may be faked (web D20).
    expect(await page.evaluate(() => getComputedStyle(document.body).fontSynthesis)).toBe('none');
    expect(errors).toEqual([]);

    const { baseURL, viewport, userAgent, deviceScaleFactor, isMobile, hasTouch } = info.project.use;
    const context = await browser.newContext({
      baseURL,
      viewport,
      userAgent,
      deviceScaleFactor,
      isMobile,
      hasTouch,
      bypassCSP: true,
    });
    const full = await context.newPage();
    await full.emulateMedia({ reducedMotion: 'reduce' });
    await full.goto(path, { waitUntil: 'networkidle' });
    await full.addStyleTag({ content: UNSKIP });
    await full.evaluate(() => document.fonts.ready);
    expect(
      await full.evaluate(
        () =>
          [...document.querySelectorAll('*')].filter(
            (e) => getComputedStyle(e).contentVisibility !== 'visible',
          ).length,
      ),
    ).toBe(0);
    const axe = await new AxeBuilder({ page: full })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
      .analyze();
    const serious = axe.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical');
    expect(serious.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`)).toEqual([]);

    await info.attach(path, { body: await full.screenshot({ fullPage: true }), contentType: 'image/png' });
    await context.close();
  });
}

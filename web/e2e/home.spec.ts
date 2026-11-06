import { expect, test, type Page } from '@playwright/test';

// The home page's interaction and layout rules that axe cannot see (web D21).

/** WCAG relative luminance contrast of two `rgb()`/`rgba()` colours (alpha ignored: opaque here). */
function contrast(a: string, b: string): number {
  const lum = (c: string) => {
    const [r, g, bl] = (c.match(/[\d.]+/g) ?? []).slice(0, 3).map((v) => {
      const s = Number(v) / 255;
      return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r! + 0.7152 * g! + 0.0722 * bl!;
  };
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi! + 0.05) / (lo! + 0.05);
}

test('keyboard focus on the ink download band shows a ring that contrasts with the band', async ({
  page,
}) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/');
  const band = page.locator('section[aria-labelledby="download-title"]');
  // Tab until focus lands on the band, as a keyboard user gets there.
  let inBand = false;
  for (let i = 0; i < 120 && !inBand; i++) {
    await page.keyboard.press('Tab');
    inBand = await band.evaluate((b) => b.contains(document.activeElement));
  }
  expect(inBand, 'focus reached the download band').toBe(true);
  const ring = await page.evaluate(() => {
    const el = document.activeElement as HTMLElement;
    const s = getComputedStyle(el);
    return {
      visible: el.matches(':focus-visible'),
      style: s.outlineStyle,
      width: parseFloat(s.outlineWidth),
      color: s.outlineColor,
    };
  });
  const background = await band.evaluate((b) => getComputedStyle(b).backgroundColor);
  expect(ring.visible).toBe(true);
  expect(ring.style).toBe('solid');
  expect(ring.width).toBeGreaterThanOrEqual(2);
  // WCAG 1.4.11 / 2.4.13: the ring against the band it is drawn on.
  expect(contrast(ring.color, background), `${ring.color} on ${background}`).toBeGreaterThanOrEqual(3);
});

async function headerBottom(page: Page) {
  const box = (await page.locator('header').first().boundingBox())!;
  return box.y + box.height;
}

for (const width of [390, 768]) {
  test(`the narrow-screen menu closes when a link in it is followed, and never covers the section (${width} px)`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/');
    const panel = page.locator('#site-menu');
    await expect(panel).toBeHidden();

    await page.getByRole('link', { name: 'Menu', exact: true }).click();
    await expect(panel).toBeVisible();
    await expect(page.getByRole('link', { name: 'Close', exact: true })).toBeVisible();
    // The call to action is in the menu on narrow screens.
    await expect(panel.getByRole('link', { name: /Join the beta/ })).toBeVisible();

    await panel.getByRole('link', { name: 'Pricing', exact: true }).click();
    await expect(panel).toBeHidden();
    const title = page.locator('#pricing-title');
    const bottom = await headerBottom(page);
    await expect
      .poll(async () => (await title.boundingBox())!.y, { message: 'the heading is below the header' })
      .toBeGreaterThanOrEqual(bottom);
    // Nothing lies over the heading: the topmost element at its centre is the heading itself.
    const onTop = await title.evaluate((h) => {
      const r = h.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + Math.min(r.width, 40) / 2, r.top + r.height / 2);
      return !!hit && h.contains(hit);
    });
    expect(onTop).toBe(true);

    // "Close" closes it where the reader is, without scrolling.
    await page.getByRole('link', { name: 'Menu', exact: true }).click();
    await expect(panel).toBeVisible();
    const y = await page.evaluate(() => scrollY);
    await page.getByRole('link', { name: 'Close', exact: true }).click();
    await expect(panel).toBeHidden();
    expect(await page.evaluate(() => scrollY)).toBe(y);
  });
}

test('the ticker moves once and stops within five seconds; under Reduce Motion it never moves', async ({
  page,
}) => {
  const track = page.locator('div[role="presentation"] > p').first();
  const timing = () =>
    track.evaluate((t) =>
      t.getAnimations().map((a) => {
        const c = a.effect!.getComputedTiming();
        return { iterations: c.iterations, endTime: Number(c.endTime) };
      }),
    );

  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await page.goto('/');
  const moving = await timing();
  expect(moving).toHaveLength(1);
  expect(moving[0]!.iterations).toBe(1);
  // Delay plus duration: done within five seconds of load (WCAG 2.2.2 needs no pause control then).
  expect(moving[0]!.endTime).toBeLessThanOrEqual(5000);

  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/');
  expect(await timing()).toEqual([]);
  expect(await track.evaluate((t) => getComputedStyle(t).transform)).toBe('none');
});

for (const width of [320, 360]) {
  test(`the hero reflows at ${width} px: no horizontal scroll, no word cut off`, async ({ page }) => {
    await page.setViewportSize({ width, height: 740 });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.goto('/');
    await page.evaluate(() => document.fonts.ready);
    const result = await page.evaluate(() => {
      const vw = document.documentElement.clientWidth;
      const cut = [
        ...document.querySelectorAll<HTMLElement>(
          '#hero-title span, section[aria-labelledby="hero-title"] p span',
        ),
      ]
        .filter((e) => e.offsetParent !== null)
        .filter((e) => {
          const r = e.getBoundingClientRect();
          return r.left < 0 || r.right > vw || e.scrollWidth > e.clientWidth + 1;
        })
        .map((e) => e.textContent);
      return { scroll: document.documentElement.scrollWidth - vw, cut };
    });
    expect(result.scroll).toBe(0);
    expect(result.cut).toEqual([]);
  });
}

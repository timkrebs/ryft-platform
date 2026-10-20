import { expect, test } from '@playwright/test';

test('the language switch keeps the page', async ({ page }) => {
  await page.goto('/science');
  await page.getByRole('banner').getByRole('link', { name: 'Deutsch' }).click();
  await expect(page).toHaveURL(/\/de\/science$/);
  await page.getByRole('banner').getByRole('link', { name: 'English' }).click();
  await expect(page).toHaveURL(/\/science$/);
});

test('the theme switch overrides the system and is remembered, without a cookie', async ({
  page,
  context,
}) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.goto('/');
  await page.getByRole('button', { name: 'Light' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(page.getByRole('button', { name: 'Light' })).toHaveAttribute('aria-pressed', 'true');
  const img = page.locator('#scores picture img').first();
  await img.scrollIntoViewIfNeeded();
  await expect.poll(() => img.evaluate((i: HTMLImageElement) => i.currentSrc)).toContain('-light-');
  await page.getByRole('button', { name: 'System' }).click();
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', /./);
  expect(await context.cookies()).toEqual([]);
});

test('the FAQ opens without JavaScript', async ({ browser }) => {
  const context = await browser.newContext({ javaScriptEnabled: false });
  const page = await context.newPage();
  await page.goto('/');
  const first = page.locator('#faq details').first();
  await first.locator('summary').click();
  await expect(first).toHaveAttribute('open', '');
  // Without JavaScript the theme switch stays hidden: no control that can't work.
  await expect(page.locator('[data-theme-switch]')).toBeHidden();
  await context.close();
});

test('the site makes no third-party requests', async ({ page }) => {
  const foreign: string[] = [];
  page.on('request', (r) => {
    const url = new URL(r.url());
    if (!['localhost', '127.0.0.1'].includes(url.hostname) && url.protocol !== 'data:') foreign.push(r.url());
  });
  for (const path of ['/', '/de', '/science', '/privacy', '/licenses'])
    await page.goto(path, { waitUntil: 'networkidle' });
  expect(foreign).toEqual([]);
});

test('the draft banner shows on every legal draft', async ({ page }) => {
  for (const path of ['/privacy', '/terms', '/impressum', '/support']) {
    await page.goto(path);
    await expect(page.getByText('Draft — not yet in force')).toBeVisible();
  }
  await page.goto('/de/privacy');
  await expect(page.getByText('Entwurf – noch nicht in Kraft')).toBeVisible();
});

import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import matter from 'gray-matter';

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

// Which legal pages are drafts comes from their front matter (`status`), as the site's
// Legal view decides it; a versioned document's root URL shows its newest version.
const LEGAL_DIR = join(process.cwd(), 'content', 'legal'); // Playwright runs from web/
const LEGAL_DOCS = ['privacy', 'terms', 'impressum', 'support'];

function legalStatus(locale: 'en' | 'de', doc: string): 'draft' | 'published' {
  let file = join(LEGAL_DIR, locale, `${doc}.mdx`);
  if (!existsSync(file)) {
    const versions = readdirSync(join(LEGAL_DIR, locale, doc)).filter((f) => f.endsWith('.mdx'));
    file = join(LEGAL_DIR, locale, doc, versions.sort().reverse()[0]!);
  }
  const status = matter(readFileSync(file, 'utf8')).data.status;
  if (status !== 'draft' && status !== 'published') throw new Error(`${file}: bad status`);
  return status;
}

test('the draft banner shows on every legal draft and on no published page', async ({ page }) => {
  const banner = { en: 'Draft — not yet in force', de: 'Entwurf – noch nicht in Kraft' } as const;
  for (const locale of ['en', 'de'] as const) {
    for (const doc of LEGAL_DOCS) {
      const status = legalStatus(locale, doc);
      await page.goto(`${locale === 'de' ? '/de' : ''}/${doc}`);
      const text = page.getByText(banner[locale]);
      if (status === 'draft') await expect(text, `${locale}/${doc} is a draft`).toBeVisible();
      else await expect(text, `${locale}/${doc} is published`).toHaveCount(0);
    }
  }
  // Whether the site has a draft left does not matter here: the draft rule itself is pinned
  // by fixtures in tests/draft.test.ts.
});

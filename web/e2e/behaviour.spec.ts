import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from '@playwright/test';
import matter from 'gray-matter';

test('the site is light only, stores nothing and sets no cookie (web D19)', async ({ page, context }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.goto('/');
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', /./);
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).colorScheme)).toBe('light');
  const img = page.locator('#scores picture img').first();
  await img.scrollIntoViewIfNeeded();
  await expect.poll(() => img.evaluate((i: HTMLImageElement) => i.currentSrc)).toContain('-light-');
  expect(await page.evaluate(() => localStorage.length)).toBe(0);
  expect(await context.cookies()).toEqual([]);
});

test('the FAQ opens without JavaScript', async ({ browser }) => {
  const context = await browser.newContext({ javaScriptEnabled: false });
  const page = await context.newPage();
  await page.goto('/');
  const first = page.locator('#faq details').first();
  await first.locator('summary').click();
  await expect(first).toHaveAttribute('open', '');
  await context.close();
});

test('the site makes no third-party requests', async ({ page }) => {
  const foreign: string[] = [];
  page.on('request', (r) => {
    const url = new URL(r.url());
    if (!['localhost', '127.0.0.1'].includes(url.hostname) && url.protocol !== 'data:') foreign.push(r.url());
  });
  for (const path of ['/', '/science', '/privacy', '/licenses'])
    await page.goto(path, { waitUntil: 'networkidle' });
  expect(foreign).toEqual([]);
});

// Which legal pages are drafts comes from their front matter (`status`), as the site's
// Legal view decides it; a versioned document's root URL shows its newest version.
const LEGAL_DIR = join(process.cwd(), 'content', 'legal'); // Playwright runs from web/
const LEGAL_DOCS = ['privacy', 'terms', 'impressum', 'support'];

function legalStatus(doc: string): 'draft' | 'published' {
  let file = join(LEGAL_DIR, 'en', `${doc}.mdx`);
  if (!existsSync(file)) {
    const versions = readdirSync(join(LEGAL_DIR, 'en', doc)).filter((f) => f.endsWith('.mdx'));
    file = join(LEGAL_DIR, 'en', doc, versions.sort().reverse()[0]!);
  }
  const status = matter(readFileSync(file, 'utf8')).data.status;
  if (status !== 'draft' && status !== 'published') throw new Error(`${file}: bad status`);
  return status;
}

test('the draft banner shows on every legal draft and on no published page', async ({ page }) => {
  for (const doc of LEGAL_DOCS) {
    const status = legalStatus(doc);
    await page.goto(`/${doc}`);
    const text = page.getByText('Draft — not yet in force');
    if (status === 'draft') await expect(text, `${doc} is a draft`).toBeVisible();
    else await expect(text, `${doc} is published`).toHaveCount(0);
  }
  // Whether the site has a draft left does not matter here: the draft rule itself is pinned
  // by fixtures in tests/draft.test.ts.
});

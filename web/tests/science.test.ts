import { existsSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import matter from 'gray-matter';
import { describe, expect, it } from 'vitest';

// /science describes one scoring model, by hand. When the platform moves to a new model
// version (SCORING.md → "ModelVersion = …"), this fails until the page is updated.
const PLATFORM = resolve(process.env.RYFT_PLATFORM_DIR ?? '../plattform');
const SCORING = join(PLATFORM, 'docs/SCORING.md');
const hasPlatform = existsSync(SCORING);
if (!hasPlatform && process.env.REQUIRE_SIBLINGS) throw new Error(`${SCORING} not found`);

const front = (locale: string) =>
  matter(readFileSync(`content/science/${locale}.mdx`, 'utf8')).data as { model: string; checked: string };

describe('science page', () => {
  it('describes the same model in English and German', () => {
    expect(front('de').model).toBe(front('en').model);
    expect(front('de').checked).toBe(front('en').checked);
  });

  it('names its model version in the text', () => {
    for (const locale of ['en', 'de']) {
      const text = readFileSync(`content/science/${locale}.mdx`, 'utf8');
      expect(text).toContain(`**${front(locale).model}**`);
    }
  });

  it.skipIf(!hasPlatform)("matches the platform's model version (SCORING.md)", () => {
    const line = readFileSync(SCORING, 'utf8').match(/ModelVersion = "([^"]+)"/);
    expect(line, 'SCORING.md has no ModelVersion line').not.toBeNull();
    expect(front('en').model, 'update content/science/*.mdx for the new model').toBe(line![1]);
  });
});

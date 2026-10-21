import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import matter from 'gray-matter';
import { describe, expect, it } from 'vitest';

// /science describes one scoring model, by hand. When the platform moves to a new model
// version (SCORING.md → "ModelVersion = …"), this fails until the page is updated.
// The platform is this repository's root (web/..), so SCORING.md is always there.
const SCORING = resolve('../docs/SCORING.md');

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

  it("matches the platform's model version (SCORING.md)", () => {
    const line = readFileSync(SCORING, 'utf8').match(/ModelVersion = "([^"]+)"/);
    expect(line, 'SCORING.md has no ModelVersion line').not.toBeNull();
    expect(front('en').model, 'update content/science/*.mdx for the new model').toBe(line![1]);
  });
});

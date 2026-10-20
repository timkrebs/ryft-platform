import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import matter from 'gray-matter';
import { describe, expect, it } from 'vitest';

// Legal content is ported from the platform's drafts, not written here. While a file
// still names its source (`portedFrom`), its text must equal the draft's, word for word;
// the user removes `portedFrom` when editing a text for publication.
const LEGAL = 'content/legal';
const RYFT = resolve(process.env.RYFT_ROOT ?? '..');
const locales = ['en', 'de'] as const;

type Front = {
  title: string;
  version: string;
  effective: string | null;
  status: 'draft' | 'published';
  versionNote?: string;
  draftNote?: string;
  portedFrom?: string;
};

function files(locale: string): string[] {
  const dir = join(LEGAL, locale);
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory()
      ? readdirSync(join(dir, e.name)).map((f) => `${e.name}/${f}`)
      : e.name.endsWith('.mdx')
        ? [e.name]
        : [],
  );
}

const read = (locale: string, rel: string) => matter(readFileSync(join(LEGAL, locale, rel), 'utf8'));

/** The readable text of a ported MDX body. */
function mdxText(body: string): string {
  return squash(
    body
      .replace(/<\/?Todo>/g, '')
      .replace(/<br \/>/g, ' ')
      .replace(/\*\*/g, '')
      .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/^#{1,6} /gm, '')
      .replace(/^- /gm, '')
      .replace(/\\([{}])/g, '$1'),
  );
}

/** The readable text of a platform draft: <main> without header, banner, meta, title, footer. */
function htmlText(html: string): string {
  const main = html.slice(html.indexOf('<main>'), html.indexOf('</main>'));
  return squash(
    main
      .replace(/<header>[\s\S]*?<\/header>/, '')
      .replace(/<footer>[\s\S]*?<\/footer>/, '')
      .replace(/<p class="(draft|meta)">[\s\S]*?<\/p>/g, '')
      .replace(/<h1>[\s\S]*?<\/h1>/, '')
      .replace(/<[^>]+>/g, ' ')
      .replace(/&amp;/g, '&')
      .replace(/&lt;/g, '<')
      .replace(/&gt;/g, '>'),
  );
}

const squash = (s: string) =>
  s
    .replace(/\s+/g, ' ')
    .replace(/ ([.,;:)])/g, '$1')
    .replace(/\( /g, '(')
    .trim();

describe('legal content', () => {
  it('exists for the same documents and versions in every language', () => {
    expect(files('de').sort()).toEqual(files('en').sort());
    expect(files('en').sort()).toEqual(
      expect.arrayContaining([
        'impressum.mdx',
        'support.mdx',
        expect.stringMatching(/^privacy\//),
        expect.stringMatching(/^terms\//),
      ]),
    );
  });

  for (const locale of locales) {
    for (const rel of files(locale)) {
      describe(`${locale}/${rel}`, () => {
        const { data, content } = read(locale, rel);
        const front = data as Front;

        it('has valid front matter', () => {
          expect(typeof front.title).toBe('string');
          expect(front.version).toMatch(/^\d{4}-\d{2}-\d{2}$/);
          expect(['draft', 'published']).toContain(front.status);
          if (front.status === 'published') expect(front.effective).toMatch(/^\d{4}-\d{2}-\d{2}$/);
          else expect(front.effective).toBeNull();
          if (rel.includes('/')) expect(rel).toBe(`${rel.split('/')[0]}/${front.version}.mdx`);
        });

        it('keeps the draft banner text while it is a draft', () => {
          if (front.status === 'draft') expect(front.draftNote).toMatch(/TODO\(user\)/);
        });

        const source = front.portedFrom ? join(RYFT, front.portedFrom) : null;
        it.skipIf(!source || !existsSync(source))('says exactly what the platform draft says', () => {
          const html = readFileSync(source!, 'utf8');
          expect(mdxText(content)).toBe(htmlText(html));
          const todos = (s: string) => (s.match(/TODO\(user\)/g) ?? []).length;
          expect(todos(content)).toBe(todos(html.replace(/<p class="draft">[\s\S]*?<\/p>/, '')));
        });
      });
    }
  }

  it('privacy and terms carry the same version in both languages', () => {
    for (const doc of ['privacy', 'terms']) {
      const versions = (l: string) => files(l).filter((f) => f.startsWith(`${doc}/`));
      expect(versions('de')).toEqual(versions('en'));
    }
  });
});

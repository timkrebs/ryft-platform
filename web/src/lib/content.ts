// MDX content (content/**/*.mdx), compiled at build time in server components.
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { evaluate } from '@mdx-js/mdx';
import matter from 'gray-matter';
import type { MDXComponents } from 'mdx/types';
import * as runtime from 'react/jsx-runtime';
import remarkGfm from 'remark-gfm';

export const CONTENT_DIR = path.join(process.cwd(), 'content');

export type MdxDocument<T> = {
  data: T;
  Content: (props: { components?: MDXComponents }) => React.ReactElement;
};

/** Reads `content/<rel>`, splits the front matter and compiles the body. */
export async function loadMdx<T>(rel: string): Promise<MdxDocument<T>> {
  const raw = await readFile(path.join(CONTENT_DIR, rel), 'utf8');
  const { data, content } = matter(raw);
  const { default: Content } = await evaluate(content, {
    ...runtime,
    remarkPlugins: [remarkGfm],
    development: false,
  });
  return { data: data as T, Content: Content as MdxDocument<T>['Content'] };
}

/** File names (without .mdx) in a content folder, newest version first for dated names. */
export async function listMdx(dir: string): Promise<string[]> {
  const entries = await readdir(path.join(CONTENT_DIR, dir));
  return entries
    .filter((f) => f.endsWith('.mdx'))
    .map((f) => f.replace(/\.mdx$/, ''))
    .sort()
    .reverse();
}

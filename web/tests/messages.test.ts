import { describe, expect, it } from 'vitest';
import de from '../messages/de.json';
import en from '../messages/en.json';

type Tree = { [key: string]: string | Tree };

function leaves(tree: Tree, prefix = ''): [string, string][] {
  return Object.entries(tree).flatMap(([k, v]) =>
    typeof v === 'string' ? [[`${prefix}${k}`, v] as [string, string]] : leaves(v, `${prefix}${k}.`),
  );
}

describe('UI strings', () => {
  const enKeys = new Map(leaves(en as Tree));
  const deKeys = new Map(leaves(de as Tree));

  it('have the same keys in English and German', () => {
    expect([...deKeys.keys()].sort()).toEqual([...enKeys.keys()].sort());
  });

  it('are never empty', () => {
    for (const [key, value] of [...enKeys, ...deKeys]) expect(value.trim(), key).not.toBe('');
  });

  it('keep {placeholders} in both languages', () => {
    for (const [key, value] of enKeys) {
      const names = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
      expect(names(deKeys.get(key) ?? ''), key).toEqual(names(value));
    }
  });

  it('address the reader as "du" in German, like the app', () => {
    for (const [key, value] of deKeys) expect(value, key).not.toMatch(/\b(Sie|Ihnen|Ihre?[mnrs]?)\b/);
  });
});

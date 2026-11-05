import { describe, expect, it } from 'vitest';
import en from '../messages/en.json';

/** Every string in a nested object or array, with its dotted path (arrays by index). */
function leaves(tree: unknown, prefix = ''): [string, string][] {
  if (typeof tree === 'string') return [[prefix.slice(0, -1), tree]];
  if (tree && typeof tree === 'object') {
    return Object.entries(tree).flatMap(([k, v]) => leaves(v, `${prefix}${k}.`));
  }
  throw new Error(`${prefix}: not a string`);
}

describe('UI strings', () => {
  const enKeys = new Map(leaves(en));

  it('are never empty', () => {
    for (const [key, value] of enKeys) expect(value.trim(), key).not.toBe('');
  });

  it('use only {placeholders} the views fill', () => {
    // Every placeholder the views fill ({holder} is split out by the Licences view).
    const known = new Set(['year', 'holder', 'n', 'license', 'version', 'date', 'model', 'values', 'today']);
    for (const [key, value] of enKeys) {
      for (const [, name] of value.matchAll(/\{(\w+)\}/g))
        expect(known.has(name), `${key}: {${name}}`).toBe(true);
    }
  });
});

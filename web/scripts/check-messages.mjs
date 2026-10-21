#!/usr/bin/env node
// messages/en.json and messages/de.json must have exactly the same keys, all non-empty.
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const ROOT = resolve(import.meta.dirname, '..');
const load = (l) => JSON.parse(readFileSync(resolve(ROOT, `messages/${l}.json`), 'utf8'));

/** Dotted keys of every leaf. */
function leaves(obj, prefix = '') {
  return Object.entries(obj).flatMap(([k, v]) =>
    v && typeof v === 'object' && !Array.isArray(v) ? leaves(v, `${prefix}${k}.`) : [[`${prefix}${k}`, v]],
  );
}

const en = new Map(leaves(load('en')));
const de = new Map(leaves(load('de')));
const problems = [
  ...[...en.keys()].filter((k) => !de.has(k)).map((k) => `missing in de: ${k}`),
  ...[...de.keys()].filter((k) => !en.has(k)).map((k) => `missing in en: ${k}`),
  ...[...en, ...de].filter(([, v]) => v === '' || v === null).map(([k]) => `empty: ${k}`),
];
if (problems.length) {
  console.error(problems.join('\n'));
  process.exit(1);
}
console.log(`messages: en and de have the same ${en.size} keys.`);

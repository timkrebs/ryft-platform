#!/usr/bin/env node
// After the build: hash every inline <script> in out/**/*.html into the CSP that nginx
// sends (nginx/csp.conf, included by nginx/headers.conf). No 'unsafe-inline' for scripts.
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { hashesOf, nginxSnippet } from './lib/csp.mjs';
import { inlineScripts } from './lib/html.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const OUT = join(ROOT, 'out');
const pages = [...walk(OUT)].filter((f) => f.endsWith('.html'));
const bodies = pages.flatMap((f) => inlineScripts(readFileSync(f, 'utf8')).map((s) => s.body));
const hashes = hashesOf(bodies);
writeFileSync(join(ROOT, 'nginx/csp.conf'), nginxSnippet(hashes, pages.length));
console.log(
  `csp: ${hashes.length} script hash(es) from ${bodies.length} inline scripts on ${pages.length} pages → nginx/csp.conf.`,
);
if (hashes.length > 8) console.warn('csp: more distinct inline scripts than expected; check the postbuild.');

function* walk(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* walk(p);
    else yield p;
  }
}

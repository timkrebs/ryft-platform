#!/usr/bin/env node
// After `next build`: turn the export into plain static HTML (docs/DECISIONS.md, D2).
//
// Next renders every page at build time; its client runtime (React, the router, the RSC
// payload) would add ~170 KB of gzipped JavaScript to pages that need none. So:
//   1. every <script> without `data-ryft` and every script preload is removed from the HTML;
//   2. the RSC payload files (*.txt next to each page, __next.*) are deleted;
//   3. JavaScript under _next/ is deleted (nothing references it any more);
//   4. the check fails if anything still points at a removed file.
// What stays: the HTML, CSS and fonts under _next/static, public/, our two inline
// scripts (src/lib/inline-scripts.ts) and, on /admin only, the admin bundle
// (src/admin, web D15); scripts/csp.mjs hashes them into the CSPs.
import { existsSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { inlineScripts, stripRuntime } from './lib/html.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const OUT = join(ROOT, 'out');
if (!existsSync(OUT)) {
  console.error('postbuild: out/ not found; run `next build` first.');
  process.exit(1);
}

// Files copied from public/ are ours and never touched.
const PUBLIC = new Set([...walk(join(ROOT, 'public'))].map((p) => relative(join(ROOT, 'public'), p)));
const KEEP_TXT = new Set(['robots.txt', ...[...PUBLIC].filter((p) => p.endsWith('.txt'))]);

let pages = 0;
let removedScripts = 0;
let removedFiles = 0;
for (const file of walk(OUT)) {
  const rel = relative(OUT, file);
  if (PUBLIC.has(rel)) continue;
  if (rel.endsWith('.html')) {
    const html = readFileSync(file, 'utf8');
    const { html: stripped, removed } = stripRuntime(html);
    removedScripts += removed;
    writeFileSync(file, stripped);
    pages++;
  } else if ((rel.endsWith('.txt') && !KEEP_TXT.has(rel)) || /(^|\/)__next\./.test(rel)) {
    rmSync(file);
    removedFiles++;
  } else if (rel.startsWith('_next/') && /\.(js|js\.map)$/.test(rel)) {
    rmSync(file);
    removedFiles++;
  }
}

// Next's internal not-found route; 404.html is the page nginx serves.
for (const p of ['_not-found.html', '_not-found', '_global-error.html', '_global-error']) {
  if (existsSync(join(OUT, p))) {
    rmSync(join(OUT, p), { recursive: true });
    removedFiles++;
  }
}
removeEmptyDirs(OUT);

// Nothing may point at what was removed.
const broken = [];
for (const file of walk(OUT)) {
  if (!file.endsWith('.html')) continue;
  const html = readFileSync(file, 'utf8');
  for (const [, ref] of html.matchAll(/(?:src|href)="(\/_next\/[^"#?]+)/g)) {
    if (!existsSync(join(OUT, ref))) broken.push(`${relative(OUT, file)} → ${ref}`);
  }
  if (/<script(?![^>]*data-ryft)/.test(html))
    broken.push(`${relative(OUT, file)} still has a runtime script`);
  // The one exception to "only the theme scripts" (web D15): /admin carries its own bundle, as
  // exactly one script, and no other page may carry it.
  const own = [...html.matchAll(/<script\b[^>]*\bdata-ryft="admin"[^>]*>/g)].length;
  const all = inlineScripts(html).length;
  if (relative(OUT, file) === 'admin.html') {
    if (own !== 1 || all !== 1)
      broken.push(`admin.html must have exactly one script, data-ryft="admin" (${own} / ${all})`);
  } else if (own > 0) {
    broken.push(`${relative(OUT, file)} carries the admin script`);
  }
}
if (!existsSync(join(OUT, 'admin.html'))) broken.push('admin.html is missing');
if (broken.length) {
  console.error(`postbuild: ${broken.length} problem(s):\n${broken.join('\n')}`);
  process.exit(1);
}
console.log(
  `postbuild: ${pages} pages as static HTML (${removedScripts} runtime scripts and ${removedFiles} files removed).`,
);

function* walk(dir) {
  if (!existsSync(dir)) return;
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(p);
    else yield p;
  }
}

function removeEmptyDirs(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) removeEmptyDirs(join(dir, entry.name));
  }
  if (dir !== OUT && readdirSync(dir).length === 0 && statSync(dir).isDirectory())
    rmSync(dir, { recursive: true });
}

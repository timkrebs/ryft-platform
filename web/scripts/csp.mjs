#!/usr/bin/env node
// After the build: hash every inline <script> in out/**/*.html into the CSP that nginx
// sends. Two snippets: nginx/csp.conf for every page except the admin page (included by
// nginx/headers.conf; those pages carry no script since web D19, so it says script-src
// 'none'), and nginx/csp-admin.conf for out/admin.html alone (included by
// nginx/headers-admin.conf). No 'unsafe-inline' for scripts in either.
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { adminNginxSnippet, hashesOf, nginxSnippet } from './lib/csp.mjs';
import { inlineScripts } from './lib/html.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const OUT = join(ROOT, 'out');
const ADMIN_PAGE = 'admin.html';
const files = [...walk(OUT)].filter((f) => f.endsWith('.html'));
const bodiesOf = (list) => list.flatMap((f) => inlineScripts(readFileSync(f, 'utf8')).map((s) => s.body));

// Marketing: every page but the admin page, so its hash never enters that policy.
const pages = files.filter((f) => relative(OUT, f) !== ADMIN_PAGE);
const hashes = hashesOf(bodiesOf(pages));
// The marketing pages ship no script (web D19; script-src 'none'): one that appears would widen
// the policy, so the build fails (before nginx/csp.conf is written) instead of hashing it in.
if (hashes.length > 0) {
  console.error(
    `csp: the marketing pages carry ${hashes.length} inline script(s); they ship none (web D19). Check the postbuild.`,
  );
  process.exit(1);
}
writeFileSync(join(ROOT, 'nginx/csp.conf'), nginxSnippet(hashes, pages.length));
console.log(
  `csp: ${hashes.length} script hash(es) from ${bodiesOf(pages).length} inline scripts on ${pages.length} pages → nginx/csp.conf.`,
);

// Admin: the one page, exactly one script.
const adminFiles = files.filter((f) => relative(OUT, f) === ADMIN_PAGE);
const adminHashes = hashesOf(bodiesOf(adminFiles));
if (adminFiles.length !== 1 || adminHashes.length !== 1) {
  console.error(
    `csp: ${ADMIN_PAGE} must exist and carry exactly one inline script (${adminHashes.length} found).`,
  );
  process.exit(1);
}
const config = JSON.parse(readFileSync(join(ROOT, 'config/admin.json'), 'utf8'));
writeFileSync(join(ROOT, 'nginx/csp-admin.conf'), adminNginxSnippet(adminHashes, config));
console.log('csp: 1 script hash for the admin page → nginx/csp-admin.conf.');

function* walk(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* walk(p);
    else yield p;
  }
}

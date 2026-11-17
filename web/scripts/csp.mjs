#!/usr/bin/env node
// After the build: hash every inline <script> in out/**/*.html into the CSP that nginx
// sends. Two snippets: nginx/csp.conf for every page except the admin page (included by
// nginx/headers.conf; those pages run no script since web D20, so script-src 'none'), and
// nginx/csp-admin.conf for out/admin.html alone (included by nginx/headers-admin.conf). No
// 'unsafe-inline' for scripts in either. Firebase Hosting gets the same two policies from
// scripts/firebase-json.mjs (web D19); the nginx files go with PR D3.
import { readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { ADMIN_PAGE, adminNginxSnippet, builtHashes, nginxSnippet } from './lib/csp.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const { pages, scripts, hashes, adminPages, adminHashes } = builtHashes(join(ROOT, 'out'));

// Marketing: every page but the admin page, so its hash never enters that policy. They run no
// script (web D20): a hash here would widen script-src 'none', so it stops the build instead.
if (hashes.length) {
  console.error(`csp: ${scripts} inline script(s) on the marketing pages; they run none (web D20).`);
  process.exit(1);
}
writeFileSync(join(ROOT, 'nginx/csp.conf'), nginxSnippet(hashes, pages));
console.log(`csp: no script on ${pages} pages, script-src 'none' → nginx/csp.conf.`);

// Admin: the one page, exactly one script.
if (adminPages !== 1 || adminHashes.length !== 1) {
  console.error(
    `csp: ${ADMIN_PAGE} must exist and carry exactly one inline script (${adminHashes.length} found).`,
  );
  process.exit(1);
}
const config = JSON.parse(readFileSync(join(ROOT, 'config/admin.json'), 'utf8'));
writeFileSync(join(ROOT, 'nginx/csp-admin.conf'), adminNginxSnippet(adminHashes, config));
console.log('csp: 1 script hash for the admin page → nginx/csp-admin.conf.');

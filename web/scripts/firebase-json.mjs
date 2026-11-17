#!/usr/bin/env node
// After the build (postbuild, after csp.mjs): write firebase.json for Firebase Hosting (web D19)
// from the export's inline-script hashes, the same two policies scripts/csp.mjs writes for nginx.
// Git-ignored, like nginx/csp.conf: it carries this build's hashes. The rules and why they look the
// way they do: scripts/lib/hosting.mjs (firebaseJson, exported here too for the tests).
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { ADMIN_PAGE, builtHashes } from './lib/csp.mjs';
import { firebaseJson } from './lib/hosting.mjs';

export { firebaseJson };

const ROOT = resolve(import.meta.dirname, '..');

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();

function main() {
  const out = join(ROOT, 'out');
  if (!existsSync(out)) fail('firebase-json: out/ not found; run `npm run build` first.');
  const { pages, hashes, adminPages, adminHashes } = builtHashes(out);
  if (adminPages !== 1 || adminHashes.length !== 1) {
    fail(`firebase-json: ${ADMIN_PAGE} must exist and carry exactly one inline script.`);
  }
  // The marketing pages run no script (web D20): their policy stays script-src 'none'.
  if (hashes.length) fail(`firebase-json: ${hashes.length} script hash(es) on the marketing pages, not 0.`);
  if (!existsSync(join(out, '.well-known/apple-app-site-association'))) {
    fail('firebase-json: out/.well-known/apple-app-site-association is missing.');
  }
  const config = JSON.parse(readFileSync(join(ROOT, 'config/admin.json'), 'utf8'));
  const json = firebaseJson({ hashes, adminHashes, config });
  writeFileSync(join(ROOT, 'firebase.json'), `${JSON.stringify(json, null, 2)}\n`);
  console.log(
    `firebase-json: ${json.hosting.headers.length} header rules; no script on ${pages} pages, 1 on ${ADMIN_PAGE} → firebase.json.`,
  );
}

function fail(message) {
  console.error(message);
  process.exit(1);
}

#!/usr/bin/env node
// The privacy policy on the site and PRIVACY_POLICY_VERSION in deploy/run/config.env (this
// repository; the published version, its home on Cloud Run) must be the same date: consent is
// recorded against that version, so content/legal/en/privacy/<version>.mdx must exist and be
// the newest, every version in content/legal/privacy-versions.json must keep its page, and the
// local stack's docker-compose.yml must agree with it (lib/policy.mjs, checkPolicy). Warns by
// default; --strict fails (CI, in the manifests job).
// Node built-ins only, so it runs from the repository root without `npm ci`:
//   node web/scripts/check-policy-version.mjs --strict
import { resolve } from 'node:path';
import { RUN_CONFIG, checkPolicy } from './lib/policy.mjs';

const siteRoot = resolve(import.meta.dirname, '..');
const repoRoot = resolve(siteRoot, '..'); // web/ is a folder of the platform repository
const strict = process.argv.includes('--strict');
const gha = !!process.env.GITHUB_ACTIONS;

const { version, problems, warnings } = checkPolicy({ siteRoot, repoRoot });
for (const msg of warnings) {
  console.log(gha ? `::warning title=privacy policy version::${msg}` : `warn: ${msg}`);
}
if (problems.length === 0) {
  console.log(`privacy policy: ${version} on the site and in ${RUN_CONFIG}.`);
} else {
  for (const msg of problems) {
    const level = strict ? 'error' : 'warning';
    console.log(
      gha ? `::${level} title=privacy policy version::${msg}` : `${strict ? 'error' : 'warn'}: ${msg}`,
    );
  }
  if (strict) process.exit(1);
}

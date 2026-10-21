#!/usr/bin/env node
// The privacy policy on the site and PRIVACY_POLICY_VERSION in deploy/k8s/base/{api,ingest}/
// configmap.yaml (this repository) must be the same date: consent is recorded against that
// version, so content/legal/{en,de}/privacy/<version>.mdx must exist and be the newest.
// Warns by default; --strict fails (CI, in the manifests job). Node built-ins only, so it
// runs from the repository root without `npm ci`:  node web/scripts/check-policy-version.mjs --strict
// RYFT_PLATFORM_DIR points at another checkout of the repository (default: web/..).
import { resolve } from 'node:path';
import { checkPolicy } from './lib/policy.mjs';

const siteRoot = resolve(import.meta.dirname, '..');
const repoRoot = resolve(siteRoot, process.env.RYFT_PLATFORM_DIR ?? '..');
const strict = process.argv.includes('--strict');

const { version, source, problems } = checkPolicy({ siteRoot, repoRoot });
if (problems.length === 0) {
  console.log(`privacy policy: ${version} on the site (en, de) and in the ConfigMaps (${source}).`);
} else {
  for (const msg of problems) {
    console.log(
      process.env.GITHUB_ACTIONS
        ? `::${strict ? 'error' : 'warning'} title=privacy policy version::${msg}`
        : `${strict ? 'error' : 'warn'}: ${msg}`,
    );
  }
  if (strict) process.exit(1);
}

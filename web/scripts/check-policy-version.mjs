#!/usr/bin/env node
// The privacy policy on the site and PRIVACY_POLICY_VERSION in deploy/run/config.env (this
// repository; the published version, its home on Cloud Run) must be the same date: consent is
// recorded against that version, so content/legal/{en,de}/privacy/<version>.mdx must exist and be
// the newest, every version in content/legal/privacy-versions.json must keep its page, and the
// copies in deploy/k8s/base/{api,ingest}/config.env (EKS, until its removal) and the local
// docker-compose.yml must agree with it. Warns by default; --strict fails (CI, in the manifests job).
// Node built-ins only, so it runs from the repository root without `npm ci`:
//   node web/scripts/check-policy-version.mjs --strict
import { existsSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { checkPolicy, versionFromEnvFile } from './lib/policy.mjs';

const siteRoot = resolve(import.meta.dirname, '..');
const repoRoot = resolve(siteRoot, '..'); // web/ is a folder of the platform repository
const strict = process.argv.includes('--strict');
const gha = !!process.env.GITHUB_ACTIONS;

// The path this check reads: deploy/run/config.env. lib/policy.mjs reads the k8s copies (and the
// pages, the list and docker-compose.yml against them); they must equal the one here.
const RUN_CONFIG = 'deploy/run/config.env';
const runPath = join(repoRoot, RUN_CONFIG);
const run = existsSync(runPath)
  ? versionFromEnvFile(readFileSync(runPath, 'utf8'))
  : { version: null, problem: 'file not found' };

const checked = checkPolicy({ siteRoot, repoRoot });
const { warnings } = checked;
const problems = [...checked.problems];
if (!run.version) {
  problems.unshift(
    `can't read the platform's version: ${RUN_CONFIG}: ${run.problem ?? 'PRIVACY_POLICY_VERSION not found'}.`,
  );
} else if (checked.version && checked.version !== run.version) {
  problems.unshift(
    `${RUN_CONFIG} has PRIVACY_POLICY_VERSION ${run.version}, ${checked.source} ${checked.version}: the k8s copies must equal ${RUN_CONFIG} (change them in the same PR).`,
  );
}
const version = run.version ?? checked.version;
for (const msg of warnings) {
  console.log(gha ? `::warning title=privacy policy version::${msg}` : `warn: ${msg}`);
}
if (problems.length === 0) {
  console.log(
    `privacy policy: ${version} on the site (en, de) and in ${RUN_CONFIG} (copies: ${checked.source}).`,
  );
} else {
  for (const msg of problems) {
    const level = strict ? 'error' : 'warning';
    console.log(
      gha ? `::${level} title=privacy policy version::${msg}` : `${strict ? 'error' : 'warn'}: ${msg}`,
    );
  }
  if (strict) process.exit(1);
}

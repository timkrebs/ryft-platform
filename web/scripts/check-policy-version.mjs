#!/usr/bin/env node
// Warns when the privacy policy on the site and the platform's PRIVACY_POLICY_VERSION
// differ: consent is recorded against the platform's version, so the page that version
// points at must exist. --strict turns the warning into a failure.
import { resolve } from 'node:path';
import { platformPolicyVersion, sitePolicyVersion } from './lib/policy.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const strict = process.argv.includes('--strict');
const site = sitePolicyVersion(ROOT);
const platform = await platformPolicyVersion({
  platformDir: resolve(ROOT, process.env.RYFT_PLATFORM_DIR ?? '../plattform'),
});
const warn = (msg) => {
  console.log(process.env.GITHUB_ACTIONS ? `::warning title=privacy policy version::${msg}` : `warn: ${msg}`);
  if (strict) process.exit(1);
};

if (!platform.version) warn(`can't read the platform's version (${platform.problem}).`);
else if (platform.version !== site) {
  warn(
    `the site's privacy policy is ${site}, the platform's PRIVACY_POLICY_VERSION is ${platform.version} (${platform.source}).`,
  );
} else console.log(`privacy policy: ${site} on the site and on the platform (${platform.source}).`);

// The privacy policy version: ours (content/legal/<locale>/privacy/<version>.mdx, both
// languages) against PRIVACY_POLICY_VERSION in this repository's api and ingest ConfigMaps,
// which consent is recorded against. Node built-ins only: CI runs the check without `npm ci`.
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

export const LOCALES = ['en', 'de'];
export const SERVICES = ['api', 'ingest'];

/** Newest version in content/legal/<locale>/privacy, or null when there is none. */
export function sitePolicyVersion(root, locale = 'en') {
  const dir = join(root, 'content/legal', locale, 'privacy');
  if (!existsSync(dir)) return null;
  const versions = readdirSync(dir)
    .filter((f) => /^\d{4}-\d{2}-\d{2}\.mdx$/.test(f))
    .map((f) => f.slice(0, -4))
    .sort();
  return versions.at(-1) ?? null;
}

/** PRIVACY_POLICY_VERSION from a kustomize ConfigMap's text. */
export function versionFromConfigMap(yaml) {
  return yaml.match(/^\s*PRIVACY_POLICY_VERSION:\s*["']?([^"'\s#]+)["']?/m)?.[1] ?? null;
}

/**
 * The platform's version from the ConfigMaps of the repository at `repoRoot`
 * (deploy/k8s/base/{api,ingest}/configmap.yaml). Both must carry it, and the same one.
 * @param {string} repoRoot
 * @returns {{version: string | null, source: string, problem?: string}}
 */
export function platformPolicyVersion(repoRoot) {
  const source = 'deploy/k8s/base/{api,ingest}/configmap.yaml';
  const found = SERVICES.map((s) => {
    const path = join(repoRoot, `deploy/k8s/base/${s}/configmap.yaml`);
    return [s, existsSync(path) ? versionFromConfigMap(readFileSync(path, 'utf8')) : null];
  });
  const missing = found.filter(([, v]) => !v).map(([s]) => s);
  if (missing.length) {
    return {
      version: null,
      source,
      problem: `PRIVACY_POLICY_VERSION not found in the ${missing.join(' and ')} ConfigMap`,
    };
  }
  const versions = [...new Set(found.map(([, v]) => v))];
  if (versions.length > 1) {
    const detail = found.map(([s, v]) => `${s}: ${v}`).join(', ');
    return { version: null, source, problem: `api and ingest disagree (${detail})` };
  }
  return { version: versions[0], source };
}

/**
 * Everything that has to hold for consent to point at a page that exists.
 * @param {{siteRoot: string, repoRoot: string}} dirs web/ and the repository root
 * @returns {{version: string | null, source: string, problems: string[]}}
 */
export function checkPolicy({ siteRoot, repoRoot }) {
  const platform = platformPolicyVersion(repoRoot);
  const problems = [];
  if (!platform.version) {
    problems.push(`can't read the platform's version: ${platform.problem}.`);
    return { version: null, source: platform.source, problems };
  }
  for (const locale of LOCALES) {
    const file = `content/legal/${locale}/privacy/${platform.version}.mdx`;
    if (!existsSync(join(siteRoot, file))) {
      problems.push(
        `${file} is missing: PRIVACY_POLICY_VERSION is ${platform.version} (${platform.source}).`,
      );
    }
    const newest = sitePolicyVersion(siteRoot, locale);
    if (newest && newest !== platform.version) {
      problems.push(
        `the newest ${locale} privacy policy is ${newest}, PRIVACY_POLICY_VERSION is ${platform.version}: change both in the same PR.`,
      );
    }
  }
  return { version: platform.version, source: platform.source, problems };
}

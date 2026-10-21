// The privacy policy version: ours (content/legal/<locale>/privacy/<version>.mdx, both
// languages) against PRIVACY_POLICY_VERSION in this repository's api and ingest ConfigMaps
// (and the local stack's docker-compose.yml), which consent is recorded against.
// content/legal/privacy-versions.json lists every version anyone may have consented to, oldest
// first, the current one last: those pages must never disappear.
// Node built-ins only: CI runs the check without `npm ci`.
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

export const LOCALES = ['en', 'de'];
export const SERVICES = ['api', 'ingest'];
const PAGE = /^\d{4}-\d{2}-\d{2}\.mdx$/;

/** Newest version in content/legal/<locale>/privacy, or null when there is none. */
export function sitePolicyVersion(root, locale = 'en') {
  const dir = join(root, 'content/legal', locale, 'privacy');
  if (!existsSync(dir)) return null;
  const versions = readdirSync(dir)
    .filter((f) => PAGE.test(f))
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
  const source = `${join(repoRoot, 'deploy/k8s/base')}/{${SERVICES.join(',')}}/configmap.yaml`;
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

/** The versions listed in content/legal/privacy-versions.json, or null when it is unusable. */
export function consentedVersions(siteRoot) {
  const path = join(siteRoot, 'content/legal/privacy-versions.json');
  try {
    const list = JSON.parse(readFileSync(path, 'utf8'));
    const ok = Array.isArray(list) && list.length > 0 && list.every((v) => /^\d{4}-\d{2}-\d{2}$/.test(v));
    return ok ? list : null;
  } catch {
    return null;
  }
}

/** The `status:` of a legal page's front matter. */
function pageStatus(file) {
  const front = readFileSync(file, 'utf8').match(/^---\r?\n([\s\S]*?)\r?\n---/)?.[1] ?? '';
  return front.match(/^status:\s*['"]?(\w+)/m)?.[1] ?? null;
}

/**
 * Everything that has to hold for consent to point at a page that exists.
 * @param {{siteRoot: string, repoRoot: string}} dirs web/ and the repository root
 * @returns {{version: string | null, source: string, problems: string[], warnings: string[]}}
 */
export function checkPolicy({ siteRoot, repoRoot }) {
  const platform = platformPolicyVersion(repoRoot);
  const problems = [];
  const warnings = [];
  if (!platform.version) {
    problems.push(`can't read the platform's version: ${platform.problem} (searched ${platform.source}).`);
    return { version: null, source: platform.source, problems, warnings };
  }
  const version = platform.version;

  const listed = consentedVersions(siteRoot);
  if (!listed) {
    problems.push(
      `content/legal/privacy-versions.json is missing or not a non-empty list of YYYY-MM-DD versions (searched ${siteRoot}).`,
    );
  } else if (listed.at(-1) !== version) {
    problems.push(
      `PRIVACY_POLICY_VERSION is ${version}, the last entry of content/legal/privacy-versions.json is ${listed.at(-1)}: add the new version to the list in the same PR.`,
    );
  }

  for (const locale of LOCALES) {
    const dir = join(siteRoot, 'content/legal', locale, 'privacy');
    const names = existsSync(dir) ? readdirSync(dir) : [];
    for (const name of names.filter((f) => !PAGE.test(f))) {
      problems.push(`content/legal/${locale}/privacy/${name}: privacy pages must be named YYYY-MM-DD.mdx.`);
    }
    for (const v of new Set([...(listed ?? []), version])) {
      if (!names.includes(`${v}.mdx`)) {
        const why = v === version ? 'PRIVACY_POLICY_VERSION' : 'a version people may have consented to';
        problems.push(`content/legal/${locale}/privacy/${v}.mdx is missing: ${v} is ${why}.`);
      }
    }
    const newest = sitePolicyVersion(siteRoot, locale);
    if (newest && newest !== version) {
      problems.push(
        `the newest ${locale} privacy policy is ${newest}, PRIVACY_POLICY_VERSION is ${version}: change both in the same PR.`,
      );
    }
    const page = join(dir, `${version}.mdx`);
    if (existsSync(page) && pageStatus(page) === 'draft') {
      warnings.push(
        `the ${locale} privacy policy ${version} is still a draft: users are asked to consent to it.`,
      );
    }
  }

  const compose = join(repoRoot, 'docker-compose.yml');
  if (existsSync(compose)) {
    const text = readFileSync(compose, 'utf8');
    const others = [...text.matchAll(/^\s*PRIVACY_POLICY_VERSION:\s*["']?([^"'\s#]+)["']?/gm)]
      .map((m) => m[1])
      .filter((v) => v !== version);
    if (others.length) {
      problems.push(
        `docker-compose.yml (local stack) has PRIVACY_POLICY_VERSION ${others[0]}, the ConfigMaps ${version}: update it with the version.`,
      );
    }
  }
  return { version, source: platform.source, problems, warnings };
}

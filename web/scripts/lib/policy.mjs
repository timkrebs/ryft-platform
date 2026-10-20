// The privacy policy version: ours (content/legal/en/privacy/<version>.mdx) against the
// platform's PRIVACY_POLICY_VERSION, which consent is recorded against.
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

/** Newest version in content/legal/<locale>/privacy. */
export function sitePolicyVersion(root, locale = 'en') {
  const dir = join(root, 'content/legal', locale, 'privacy');
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
 * The platform's version and where it came from, trying in order: the environment
 * (PLATFORM_PRIVACY_POLICY_VERSION), the platform's ConfigMaps next to this repo, and a
 * public JSON document (PLATFORM_LEGAL_URL, proposed in docs/HANDOFF.md).
 * @param {{env?: Record<string, string | undefined>, platformDir?: string, fetchImpl?: typeof fetch}} [options]
 * @returns {Promise<{version: string | null, source: string | null, problem?: string}>}
 */
export async function platformPolicyVersion({ env = process.env, platformDir, fetchImpl = fetch } = {}) {
  if (env.PLATFORM_PRIVACY_POLICY_VERSION) {
    return { version: env.PLATFORM_PRIVACY_POLICY_VERSION, source: 'PLATFORM_PRIVACY_POLICY_VERSION' };
  }
  if (platformDir) {
    const maps = ['api', 'ingest'].map((s) => join(platformDir, `deploy/k8s/base/${s}/configmap.yaml`));
    const found = maps.filter(existsSync).map((p) => [p, versionFromConfigMap(readFileSync(p, 'utf8'))]);
    if (found.length) {
      const versions = [...new Set(found.map(([, v]) => v))];
      if (versions.length > 1) {
        return {
          version: null,
          source: 'platform ConfigMaps',
          problem: `api and ingest disagree: ${versions.join(' vs ')}`,
        };
      }
      return { version: versions[0], source: 'platform ConfigMaps (api, ingest)' };
    }
  }
  if (env.PLATFORM_LEGAL_URL) {
    const res = await fetchImpl(env.PLATFORM_LEGAL_URL);
    if (!res.ok) return { version: null, source: env.PLATFORM_LEGAL_URL, problem: `HTTP ${res.status}` };
    const body = await res.json();
    return { version: body.privacy_policy_version ?? null, source: env.PLATFORM_LEGAL_URL };
  }
  return {
    version: null,
    source: null,
    problem: 'no source: set PLATFORM_PRIVACY_POLICY_VERSION or check out ../plattform',
  };
}

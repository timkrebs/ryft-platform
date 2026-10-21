import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  checkPolicy,
  platformPolicyVersion,
  sitePolicyVersion,
  versionFromConfigMap,
} from '../scripts/lib/policy.mjs';

/** A repository root with the api and ingest ConfigMaps (a service mapped to null gets none). */
function repo(versions: Record<string, string | null>) {
  const dir = mkdtempSync(join(tmpdir(), 'ryft-repo-'));
  for (const [svc, v] of Object.entries(versions)) {
    if (v === null) continue;
    mkdirSync(join(dir, 'deploy/k8s/base', svc), { recursive: true });
    writeFileSync(
      join(dir, 'deploy/k8s/base', svc, 'configmap.yaml'),
      `data:\n  PRIVACY_POLICY_VERSION: "${v}"\n`,
    );
  }
  return dir;
}

/** A site root (web/) with the privacy texts of each locale. */
function site(pages: Record<string, string[]>) {
  const dir = mkdtempSync(join(tmpdir(), 'ryft-site-'));
  for (const [locale, versions] of Object.entries(pages)) {
    mkdirSync(join(dir, 'content/legal', locale, 'privacy'), { recursive: true });
    for (const v of versions)
      writeFileSync(join(dir, 'content/legal', locale, 'privacy', `${v}.mdx`), '---\n---\n');
  }
  return dir;
}

const V = '2026-10-20';
const both = (v: string) => repo({ api: v, ingest: v });

describe('privacy policy version check', () => {
  it('reads the newest version on the site', () => {
    expect(sitePolicyVersion('.')).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(sitePolicyVersion('.', 'de')).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(sitePolicyVersion(site({ en: ['2026-01-01', '2026-10-20', '2026-03-03'] }))).toBe(V);
    expect(sitePolicyVersion(site({ en: [] }), 'de')).toBeNull();
  });

  it('parses quoted and unquoted ConfigMap values', () => {
    expect(versionFromConfigMap('data:\n  PRIVACY_POLICY_VERSION: "2026-10-20"\n')).toBe(V);
    expect(versionFromConfigMap('  PRIVACY_POLICY_VERSION: 2026-11-01 # bump')).toBe('2026-11-01');
    expect(versionFromConfigMap('OTHER: 1')).toBeNull();
  });

  it('reads the ConfigMaps of the repository', () => {
    expect(platformPolicyVersion(both(V))).toMatchObject({ version: V });
  });

  it('is ok when both languages have the version and it is the newest', () => {
    const r = checkPolicy({ siteRoot: site({ en: ['2026-01-01', V], de: [V] }), repoRoot: both(V) });
    expect(r).toMatchObject({ version: V, problems: [] });
  });

  it('fails when the German page is missing', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [] }), repoRoot: both(V) });
    expect(r.problems).toHaveLength(1);
    expect(r.problems[0]).toMatch(/content\/legal\/de\/privacy\/2026-10-20\.mdx is missing/);
  });

  it('fails when the English page is missing', () => {
    const r = checkPolicy({ siteRoot: site({ en: [], de: [V] }), repoRoot: both(V) });
    expect(r.problems.join('\n')).toMatch(/content\/legal\/en\/privacy\/2026-10-20\.mdx is missing/);
  });

  it('fails when the ConfigMaps are ahead of the site', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }), repoRoot: both('2026-11-01') });
    expect(r.problems.join('\n')).toMatch(/2026-11-01\.mdx is missing/);
    expect(r.problems.join('\n')).toMatch(
      /newest en privacy policy is 2026-10-20, PRIVACY_POLICY_VERSION is 2026-11-01/,
    );
  });

  it('fails when the site is ahead of the ConfigMaps (a new text nobody consents to)', () => {
    const r = checkPolicy({
      siteRoot: site({ en: [V, '2026-11-01'], de: [V, '2026-11-01'] }),
      repoRoot: both(V),
    });
    expect(r.problems).toHaveLength(2);
    expect(r.problems.join('\n')).toMatch(/newest de privacy policy is 2026-11-01/);
  });

  it('fails when api and ingest disagree', () => {
    const r = checkPolicy({
      siteRoot: site({ en: [V], de: [V] }),
      repoRoot: repo({ api: V, ingest: '2026-11-01' }),
    });
    expect(r.version).toBeNull();
    expect(r.problems.join('\n')).toMatch(/api and ingest disagree/);
  });

  it('fails when a ConfigMap has no version', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }), repoRoot: repo({ api: V, ingest: null }) });
    expect(r.problems.join('\n')).toMatch(/not found in the ingest ConfigMap/);
  });
});

describe('check-policy-version.mjs', () => {
  it('passes --strict in this repository', () => {
    const script = resolve('scripts/check-policy-version.mjs');
    const out = execFileSync('node', [script, '--strict'], { encoding: 'utf8' });
    expect(out).toMatch(/^privacy policy: \d{4}-\d{2}-\d{2} on the site/);
  });
});

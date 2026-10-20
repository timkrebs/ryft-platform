import { mkdtempSync, mkdirSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { platformPolicyVersion, sitePolicyVersion, versionFromConfigMap } from '../scripts/lib/policy.mjs';

function platform(versions: Record<string, string>) {
  const dir = mkdtempSync(join(tmpdir(), 'ryft-platform-'));
  for (const [svc, v] of Object.entries(versions)) {
    mkdirSync(join(dir, 'deploy/k8s/base', svc), { recursive: true });
    writeFileSync(
      join(dir, 'deploy/k8s/base', svc, 'configmap.yaml'),
      `data:\n  PRIVACY_POLICY_VERSION: "${v}"\n`,
    );
  }
  return dir;
}

describe('privacy policy version check', () => {
  it('reads the newest version on the site', () => {
    expect(sitePolicyVersion('.')).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it('parses quoted and unquoted ConfigMap values', () => {
    expect(versionFromConfigMap('data:\n  PRIVACY_POLICY_VERSION: "2026-10-20"\n')).toBe('2026-10-20');
    expect(versionFromConfigMap('  PRIVACY_POLICY_VERSION: 2026-11-01 # bump')).toBe('2026-11-01');
    expect(versionFromConfigMap('OTHER: 1')).toBeNull();
  });

  it('prefers the environment', async () => {
    const r = await platformPolicyVersion({
      env: { PLATFORM_PRIVACY_POLICY_VERSION: '2027-01-01' },
      platformDir: platform({ api: '2026-10-20' }),
    });
    expect(r).toMatchObject({ version: '2027-01-01', source: 'PLATFORM_PRIVACY_POLICY_VERSION' });
  });

  it('reads the platform ConfigMaps and reports a disagreement', async () => {
    expect(
      (
        await platformPolicyVersion({
          env: {},
          platformDir: platform({ api: '2026-10-20', ingest: '2026-10-20' }),
        })
      ).version,
    ).toBe('2026-10-20');
    const split = await platformPolicyVersion({
      env: {},
      platformDir: platform({ api: '2026-10-20', ingest: '2026-11-01' }),
    });
    expect(split.version).toBeNull();
    expect(split.problem).toMatch(/disagree/);
  });

  it('falls back to the public JSON document', async () => {
    const fetchImpl = (async () =>
      new Response(JSON.stringify({ privacy_policy_version: '2026-10-20' }))) as typeof fetch;
    const r = await platformPolicyVersion({
      env: { PLATFORM_LEGAL_URL: 'https://example.test/legal' },
      platformDir: '/nope',
      fetchImpl,
    });
    expect(r.version).toBe('2026-10-20');
  });

  it('says when there is no source', async () => {
    expect((await platformPolicyVersion({ env: {}, platformDir: '/nope' })).problem).toMatch(/no source/);
  });
});

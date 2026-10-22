import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  checkPolicy,
  platformPolicyVersion,
  sitePolicyVersion,
  versionFromEnvFile,
} from '../scripts/lib/policy.mjs';

/** A repository root with the api and ingest config.env (a service mapped to null gets none). */
function repo(versions: Record<string, string | null>, line = (v: string) => `PRIVACY_POLICY_VERSION=${v}`) {
  const dir = mkdtempSync(join(tmpdir(), 'ryft-repo-'));
  for (const [svc, v] of Object.entries(versions)) {
    if (v === null) continue;
    mkdirSync(join(dir, 'deploy/k8s/base', svc), { recursive: true });
    writeFileSync(join(dir, 'deploy/k8s/base', svc, 'config.env'), `# comment\nOTHER=1\n${line(v)}\n`);
  }
  return dir;
}

/** A site root (web/) with the privacy texts of each locale and privacy-versions.json. */
function site(pages: Record<string, string[]>, list: string[] | null = ['2026-10-20'], status = 'published') {
  const dir = mkdtempSync(join(tmpdir(), 'ryft-site-'));
  for (const [locale, names] of Object.entries(pages)) {
    mkdirSync(join(dir, 'content/legal', locale, 'privacy'), { recursive: true });
    for (const v of names) {
      const file = v.includes('.') ? v : `${v}.mdx`;
      writeFileSync(join(dir, 'content/legal', locale, 'privacy', file), `---\nstatus: ${status}\n---\n`);
    }
  }
  if (list) writeFileSync(join(dir, 'content/legal/privacy-versions.json'), JSON.stringify(list));
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

  it('parses config.env lines of exactly one shape', () => {
    expect(versionFromEnvFile('PRIVACY_POLICY_VERSION=2026-10-20\n')).toEqual({ version: V });
    expect(versionFromEnvFile('A=1\r\nPRIVACY_POLICY_VERSION=2026-10-20\r\n')).toEqual({ version: V });
    expect(versionFromEnvFile('OTHER=1').version).toBeNull();
    expect(versionFromEnvFile('OTHER=1').problem).toBeUndefined();
  });

  it('ignores a commented-out version line', () => {
    const text = '# PRIVACY_POLICY_VERSION=2026-01-01\nPRIVACY_POLICY_VERSION=2026-10-20\n';
    expect(versionFromEnvFile(text)).toEqual({ version: V });
    expect(versionFromEnvFile('# PRIVACY_POLICY_VERSION=2026-01-01\n')).toEqual({ version: null });
  });

  it.each([
    ['quoted', 'PRIVACY_POLICY_VERSION="2026-10-20"'],
    ['single-quoted', "PRIVACY_POLICY_VERSION='2026-10-20'"],
    ['spaced', 'PRIVACY_POLICY_VERSION = 2026-10-20'],
    ['trailing space', 'PRIVACY_POLICY_VERSION=2026-10-20 '],
    ['inline-commented', 'PRIVACY_POLICY_VERSION=2026-10-20 # bump'],
    ['not a date', 'PRIVACY_POLICY_VERSION=latest'],
    ['yaml style', 'PRIVACY_POLICY_VERSION: 2026-10-20'],
  ])('reports a %s line as a problem, not a miss', (_name, line) => {
    const r = versionFromEnvFile(`${line}\n`);
    expect(r.version).toBeNull();
    expect(r.problem).toMatch(/not exactly PRIVACY_POLICY_VERSION=YYYY-MM-DD/);
    const p = platformPolicyVersion(repo({ api: V, ingest: V }, () => line));
    expect(p.version).toBeNull();
    expect(p.problem).toContain('deploy/k8s/base/api/config.env');
    expect(p.problem).toContain('deploy/k8s/base/ingest/config.env');
  });

  it('reports a date that is not in the calendar', () => {
    const r = versionFromEnvFile('PRIVACY_POLICY_VERSION=2026-11-31\n');
    expect(r.version).toBeNull();
    expect(r.problem).toMatch(/not a calendar date/);
    expect(versionFromEnvFile('PRIVACY_POLICY_VERSION=2028-02-29\n').version).toBe('2028-02-29');
    expect(versionFromEnvFile('PRIVACY_POLICY_VERSION=2026-02-29\n').version).toBeNull();
  });

  it('rejects an impossible date in privacy-versions.json', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }, ['2026-11-31', V]), repoRoot: both(V) });
    expect(r.problems.join('\n')).toMatch(/privacy-versions\.json is missing or not a non-empty list/);
  });

  it('reports an indented version line, and ignores other keys with the same prefix', () => {
    const r = versionFromEnvFile('  PRIVACY_POLICY_VERSION=2026-10-20\n');
    expect(r.version).toBeNull();
    expect(r.problem).toMatch(/not exactly/);
    const other = 'PRIVACY_POLICY_VERSION_NEXT=2026-11-01\n';
    expect(versionFromEnvFile(`${other}PRIVACY_POLICY_VERSION=2026-10-20\n`)).toEqual({ version: V });
    expect(versionFromEnvFile(other)).toEqual({ version: null });
  });

  it('reports a version set twice', () => {
    const text = 'PRIVACY_POLICY_VERSION=2026-10-20\nPRIVACY_POLICY_VERSION=2026-11-01\n';
    expect(versionFromEnvFile(text).problem).toMatch(/more than once/);
  });

  it('reads the config.env files of the repository', () => {
    expect(platformPolicyVersion(both(V))).toMatchObject({ version: V });
  });

  it('names the file when one is missing', () => {
    const p = platformPolicyVersion(repo({ api: V, ingest: null }));
    expect(p.version).toBeNull();
    expect(p.problem).toBe('deploy/k8s/base/ingest/config.env: file not found');
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

  it('fails when config.env is ahead of the site', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }), repoRoot: both('2026-11-01') });
    expect(r.problems.join('\n')).toMatch(/2026-11-01\.mdx is missing/);
    expect(r.problems.join('\n')).toMatch(
      /newest en privacy policy is 2026-10-20, PRIVACY_POLICY_VERSION is 2026-11-01/,
    );
  });

  it('fails when the site is ahead of config.env (a new text nobody consents to)', () => {
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

  it('fails when a config.env is missing', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }), repoRoot: repo({ api: V, ingest: null }) });
    expect(r.problems.join('\n')).toMatch(/deploy\/k8s\/base\/ingest\/config\.env: file not found/);
  });
});

describe('privacy versions that users consented to', () => {
  it('fails when an old version loses its page (a renamed file)', () => {
    const r = checkPolicy({
      siteRoot: site({ en: ['2026-11-01'], de: ['2026-11-01'] }, ['2026-10-20', '2026-11-01']),
      repoRoot: both('2026-11-01'),
    });
    expect(r.problems.join('\n')).toMatch(/en\/privacy\/2026-10-20\.mdx is missing/);
    expect(r.problems.join('\n')).toMatch(/de\/privacy\/2026-10-20\.mdx is missing/);
  });

  it('is ok when the old version is kept and the list ends with the current one', () => {
    const both2 = ['2026-10-20', '2026-11-01'];
    const r = checkPolicy({
      siteRoot: site({ en: both2, de: both2 }, both2),
      repoRoot: both('2026-11-01'),
    });
    expect(r.problems).toEqual([]);
  });

  it('fails when config.env is not the last entry of the list', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }, ['2026-01-01']), repoRoot: both(V) });
    expect(r.problems.join('\n')).toMatch(
      /last entry of content\/legal\/privacy-versions\.json is 2026-01-01/,
    );
  });

  it('fails without the list', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }, null), repoRoot: both(V) });
    expect(r.problems.join('\n')).toMatch(/privacy-versions\.json is missing/);
  });

  it('fails on a privacy page with another name', () => {
    const r = checkPolicy({ siteRoot: site({ en: [V, 'draft.mdx'], de: [V] }), repoRoot: both(V) });
    expect(r.problems.join('\n')).toMatch(/en\/privacy\/draft\.mdx: privacy pages must be named/);
  });

  it('ignores dotfiles such as .DS_Store, but not other non-date pages', () => {
    const dir = site({ en: [V, '.DS_Store'], de: [V] });
    expect(checkPolicy({ siteRoot: dir, repoRoot: both(V) }).problems).toEqual([]);
    expect(sitePolicyVersion(dir)).toBe(V);
  });

  it('warns, but does not fail, while the consented page is a draft', () => {
    const r = checkPolicy({
      siteRoot: site({ en: [V], de: [V] }, ['2026-10-20'], 'draft'),
      repoRoot: both(V),
    });
    expect(r.problems).toEqual([]);
    expect(r.warnings).toHaveLength(2);
    expect(r.warnings[0]).toMatch(/still a draft/);
  });

  it("fails when docker-compose.yml's version differs", () => {
    const repoRoot = both(V);
    writeFileSync(join(repoRoot, 'docker-compose.yml'), '    PRIVACY_POLICY_VERSION: "2026-01-01"\n');
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }), repoRoot });
    expect(r.problems.join('\n')).toMatch(/docker-compose\.yml .* 2026-01-01/);
    writeFileSync(join(repoRoot, 'docker-compose.yml'), '    PRIVACY_POLICY_VERSION: "2026-10-20"\n');
    expect(checkPolicy({ siteRoot: site({ en: [V], de: [V] }), repoRoot }).problems).toEqual([]);
  });

  it('names the searched folder when config.env is not there', () => {
    const empty = mkdtempSync(join(tmpdir(), 'ryft-empty-'));
    const r = checkPolicy({ siteRoot: site({ en: [V], de: [V] }), repoRoot: empty });
    expect(r.problems[0]).toContain(empty);
  });
});

describe('check-policy-version.mjs', () => {
  it('passes --strict in this repository', () => {
    const script = resolve('scripts/check-policy-version.mjs');
    const out = execFileSync('node', [script, '--strict'], { encoding: 'utf8' });
    expect(out).toMatch(/^privacy policy: \d{4}-\d{2}-\d{2} on the site/m);
  });
});

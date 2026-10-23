import { execFileSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

// Throwaway repositories laid out like the monorepo (web/ next to backend/), for the tests
// that run the brand guard and the git hooks. Fixtures are ROT13, so no test file spells
// the name it tests for.
export const rot13 = (s: string) =>
  s.replace(/[a-z]/gi, (c) => {
    const base = c <= 'Z' ? 65 : 97;
    return String.fromCharCode(((c.charCodeAt(0) - base + 13) % 26) + base);
  });
export const TERM = rot13('jubbc');

/** The environment without the variables git exports to hooks (tests may run inside one). */
export function cleanEnv(extra: Record<string, string> = {}) {
  const env = { ...process.env };
  for (const k of ['GIT_DIR', 'GIT_INDEX_FILE', 'GIT_WORK_TREE', 'GIT_PREFIX']) delete env[k];
  return { ...env, ...extra };
}

export function git(cwd: string, ...args: string[]) {
  return execFileSync(
    'git',
    ['-c', 'user.name=t', '-c', 'user.email=t@example.test', '-c', 'commit.gpgsign=false', ...args],
    {
      cwd,
      env: cleanEnv(),
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  );
}

export function write(repo: string, rel: string, text: string) {
  mkdirSync(dirname(join(repo, rel)), { recursive: true });
  writeFileSync(join(repo, rel), text);
}

/** A repository with the brand guard (and optionally the hooks) committed, plus a first commit. */
export function makeRepo({ hooks = false } = {}) {
  const repo = mkdtempSync(join(tmpdir(), 'ryft-brand-'));
  git(repo, 'init', '-q', '-b', 'main');
  for (const f of ['scripts/check-brand.mjs', 'scripts/lib/brand.mjs', 'config/brand-denylist.json']) {
    mkdirSync(dirname(join(repo, 'web', f)), { recursive: true });
    cpSync(resolve(f), join(repo, 'web', f));
  }
  if (hooks) {
    cpSync(resolve('../.githooks'), join(repo, '.githooks'), { recursive: true });
    // The pre-commit hook runs the byte check on every staged file (it needs python3).
    mkdirSync(join(repo, 'scripts'), { recursive: true });
    cpSync(resolve('../scripts/check_bytes.sh'), join(repo, 'scripts/check_bytes.sh'));
    mkdirSync(join(repo, 'scripts/lib'), { recursive: true });
    cpSync(resolve('../scripts/lib/charpolicy.py'), join(repo, 'scripts/lib/charpolicy.py'));
  }
  write(repo, 'README.md', 'x\n');
  git(repo, 'add', '-A');
  git(repo, '-c', 'core.hooksPath=/dev/null', 'commit', '-q', '-m', 'init');
  return repo;
}

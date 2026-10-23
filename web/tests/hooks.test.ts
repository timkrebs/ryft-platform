import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import { TERM, cleanEnv, git, makeRepo, write } from './helpers/repo';

// The shell layer: .githooks/{pre-commit,commit-msg} driven by real `git commit`s in a
// throwaway repository, in the main checkout and in a linked worktree.
// Every case spawns git and node: no 5 s default on a busy runner.
vi.setConfig({ testTimeout: 30_000, hookTimeout: 30_000 });

let repo: string;
let worktree: string;

function commit(cwd: string, message: string, env: Record<string, string> = {}) {
  const hooks = join(repo, '.githooks');
  return spawnSync(
    'git',
    [
      '-c',
      `core.hooksPath=${hooks}`,
      '-c',
      'user.name=t',
      '-c',
      'user.email=t@example.test',
      'commit',
      '-q',
      '-m',
      message,
    ],
    {
      cwd,
      encoding: 'utf8',
      env: cleanEnv(env),
    },
  );
}

beforeAll(() => {
  repo = makeRepo({ hooks: true });
  worktree = join(repo, '..', `hooks-wt-${Date.now()}`);
  git(repo, 'worktree', 'add', '-q', '-b', 'wt', worktree);
});

describe.each([
  ['main checkout', () => repo],
  ['linked worktree', () => worktree],
])('git hooks in the %s', (_, dir) => {
  it('lets a staged file outside web/ with the term through', () => {
    write(dir(), 'backend/a.go', `// ${TERM}\n`);
    git(dir(), 'add', 'backend/a.go');
    const r = commit(dir(), 'backend: a change');
    expect(r.status, r.stdout + r.stderr).toBe(0);
  });

  it('blocks a staged file under web/ with the term', () => {
    write(dir(), 'web/src/a.ts', `// ${TERM}\n`);
    git(dir(), 'add', 'web/src/a.ts');
    const r = commit(dir(), 'web: a change');
    expect(r.status).toBe(1);
    expect(r.stdout + r.stderr).toMatch(/^ERROR src\/a\.ts:1/m);
    git(dir(), 'reset', '-q', '--', 'web/src/a.ts');
  });

  it('blocks a commit message with the term, also for a file outside web/', () => {
    write(dir(), 'backend/b.go', 'package b\n');
    git(dir(), 'add', 'backend/b.go');
    const r = commit(dir(), `backend: ${TERM}`);
    expect(r.status).toBe(1);
    expect(r.stdout + r.stderr).not.toContain(TERM);
    expect(commit(dir(), 'backend: b').status).toBe(0);
  });
});

describe('git hooks without node', () => {
  // A PATH with git, sh and dirname but no node, and no place the hooks probe. The byte check
  // (bash, env, python3) is there: it runs first and must pass, so the node messages are the ones seen.
  const bin = mkdtempSync(join(tmpdir(), 'ryft-nonode-'));
  for (const tool of ['git', 'sh', 'dirname', 'bash', 'env', 'python3']) {
    const found = spawnSync('sh', ['-c', `command -v ${tool}`], { encoding: 'utf8' }).stdout.trim();
    symlinkSync(found, join(bin, tool));
  }
  const env = { PATH: bin, RYFT_HOOKS_NODE_DIRS: '' };

  it('fails closed on a message, and says how to go on', () => {
    write(repo, 'backend/c.go', 'package c\n');
    git(repo, 'add', 'backend/c.go');
    const r = commit(repo, 'backend: c', env);
    expect(r.status).toBe(1);
    expect(r.stderr).toMatch(/Install Node \(web\/\.nvmrc\), or commit with --no-verify/);
  });

  it('fails closed on staged web/ files', () => {
    write(repo, 'web/src/d.ts', 'export {};\n');
    git(repo, 'add', 'web/src/d.ts');
    const r = commit(repo, 'web: d', env);
    expect(r.status).toBe(1);
    expect(r.stderr).toMatch(/pre-commit: node not found/);
  });

  it('finds node in a probed folder', () => {
    const node = spawnSync('sh', ['-c', 'command -v node'], { encoding: 'utf8' }).stdout.trim();
    const probed = join(bin, 'probed');
    mkdirSync(probed);
    symlinkSync(node, join(probed, 'node'));
    const r = commit(repo, 'web: d', { ...env, RYFT_HOOKS_NODE_DIRS: probed });
    expect(r.status, r.stdout + r.stderr).toBe(0);
  });
});

describe('git hooks and the byte check', () => {
  const tools = (names: string[]) => {
    const bin = mkdtempSync(join(tmpdir(), 'ryft-tools-'));
    for (const tool of names) {
      const found = spawnSync('sh', ['-c', `command -v ${tool}`], { encoding: 'utf8' }).stdout.trim();
      symlinkSync(found, join(bin, tool));
    }
    return bin;
  };
  const node = spawnSync('sh', ['-c', 'command -v node'], { encoding: 'utf8' }).stdout.trim();

  it('refuses a staged hidden line break before the brand check', () => {
    write(repo, 'backend/ls.go', '// a\u2028b\n');
    git(repo, 'add', 'backend/ls.go');
    const r = commit(repo, 'backend: ls');
    expect(r.status).toBe(1);
    expect(r.stderr).toMatch(/U\+2028/);
    git(repo, 'reset', '-q', '--', 'backend/ls.go');
  });

  it('fails closed without python3, and says so', () => {
    const bin = tools(['git', 'sh', 'dirname', 'bash', 'env']);
    const probed = join(bin, 'probed');
    mkdirSync(probed);
    symlinkSync(node, join(probed, 'node'));
    write(repo, 'backend/py.go', 'package py\n');
    git(repo, 'add', 'backend/py.go');
    const r = commit(repo, 'backend: py', { PATH: bin, RYFT_HOOKS_NODE_DIRS: probed });
    expect(r.status).toBe(1);
    expect(r.stderr).toMatch(/python3 not found/);
    git(repo, 'reset', '-q', '--', 'backend/py.go');
  });

  it('fails closed when scripts/check_bytes.sh is missing', () => {
    const lone = makeRepo({ hooks: true });
    rmSync(join(lone, 'scripts/check_bytes.sh'));
    write(lone, 'backend/m.go', 'package m\n');
    git(lone, 'add', 'backend/m.go');
    const hooks = join(lone, '.githooks');
    const r = spawnSync(
      'git',
      [
        '-c',
        `core.hooksPath=${hooks}`,
        '-c',
        'user.name=t',
        '-c',
        'user.email=t@example.test',
        'commit',
        '-q',
        '-m',
        'backend: m',
      ],
      {
        cwd: lone,
        encoding: 'utf8',
        env: cleanEnv(),
      },
    );
    expect(r.status).toBe(1);
    expect(r.stderr).toMatch(/check_bytes\.sh/);
  });
});

import { spawnSync } from 'node:child_process';
import { join } from 'node:path';
import { beforeAll, describe, expect, it } from 'vitest';
import { TERM, cleanEnv, git, makeRepo, write } from './helpers/repo';

// scripts/check-brand.mjs in throwaway repositories laid out like the monorepo.
let repo: string;
const run = (cwd: string, args: string[], env: Record<string, string> = {}) =>
  spawnSync('node', ['web/scripts/check-brand.mjs', ...args], { cwd, encoding: 'utf8', env: cleanEnv(env) });

beforeAll(() => {
  repo = makeRepo();
});

describe('check-brand.mjs --staged', () => {
  it('ignores staged files outside web/', () => {
    write(repo, 'backend/main.go', `// ${TERM}\n`);
    git(repo, 'add', 'backend/main.go');
    const r = run(repo, ['--staged']);
    expect(r.status).toBe(0);
    expect(r.stdout).toContain('0 inputs');
    git(repo, 'reset', '-q');
  });

  it('blocks a staged file under web/, naming it relative to web/', () => {
    write(repo, 'web/src/page.ts', `// ${TERM}\n`);
    git(repo, 'add', 'web/src/page.ts');
    const r = run(repo, ['--staged']);
    expect(r.status).toBe(1);
    expect(r.stdout).toMatch(/^ERROR src\/page\.ts:1/m);
    expect(r.stdout).not.toContain('web/');
    expect(r.stdout).not.toContain(TERM);
    git(repo, 'reset', '-q');
  });

  it('checks the staged content, not the working tree', () => {
    write(repo, 'web/src/clean.ts', 'export {};\n');
    git(repo, 'add', 'web/src/clean.ts');
    write(repo, 'web/src/clean.ts', `// ${TERM}\n`);
    expect(run(repo, ['--staged']).status).toBe(0);
    git(repo, 'reset', '-q');
  });

  it('blocks a file name under web/', () => {
    write(repo, `web/src/${TERM}.ts`, 'export {};\n');
    git(repo, 'add', `web/src/${TERM}.ts`);
    expect(run(repo, ['--staged']).status).toBe(1);
    git(repo, 'reset', '-q');
  });

  it('stays scoped to web/ in a linked worktree, where git exports GIT_DIR to hooks', () => {
    const wt = join(repo, '..', `wt-${Date.now()}`);
    git(repo, 'worktree', 'add', '-q', '-b', `b${Date.now()}`, wt);
    const gitDir = git(wt, 'rev-parse', '--absolute-git-dir').trim();
    write(wt, 'backend/main.go', `// ${TERM}\n`);
    git(wt, 'add', 'backend/main.go');
    const ok = run(wt, ['--staged'], { GIT_DIR: gitDir });
    expect(ok.status, ok.stdout + ok.stderr).toBe(0);
    write(wt, 'web/src/page.ts', `// ${TERM}\n`);
    git(wt, 'add', 'web/src/page.ts');
    const r = run(wt, ['--staged'], { GIT_DIR: gitDir });
    expect(r.status).toBe(1);
    expect(r.stdout).toMatch(/^ERROR src\/page\.ts:1/m);
    expect(r.stdout).not.toMatch(/main\.go/);
  });
});

describe('check-brand.mjs --message (CI: the whole text, nothing stripped)', () => {
  const msg = (name: string, text: string) => {
    write(repo, name, text);
    return join(repo, name);
  };
  const SCISSORS = '# ------------------------ >8 ------------------------';

  it('blocks a text with the name and passes a clean one', () => {
    expect(run(repo, ['--message', msg('m1', `fix: ${TERM}\n`)]).status).toBe(1);
    expect(run(repo, ['--message', msg('m2', 'fix: something\n')]).status).toBe(0);
  });

  it('scans below a literal scissors line and in comment lines', () => {
    expect(run(repo, ['--message', msg('m3', `fix: ok\n\n${SCISSORS}\n${TERM}\n`)]).status).toBe(1);
    expect(
      run(repo, ['--message', msg('m4', `fix: ok\n\n# Please enter the commit message\n# ${TERM}\n`)]).status,
    ).toBe(1);
  });
});

describe('check-brand.mjs --commit-msg (the hook: what git will record)', () => {
  const msg = (name: string, text: string) => {
    write(repo, name, text);
    return join(repo, name);
  };
  const hook = (file: string) => run(repo, ['--commit-msg', file]).status;
  const SCISSORS = '# ------------------------ >8 ------------------------';
  const HELP = '# Please enter the commit message for your changes.';

  it('blocks a commit message with the name and passes a clean one', () => {
    expect(hook(msg('h1', `fix: ${TERM}\n`))).toBe(1);
    expect(hook(msg('h2', 'fix: something\n'))).toBe(0);
  });

  it('ignores the diff below the scissors line of the editor template (git commit -v)', () => {
    const text = `fix: something\n\n${HELP}\n${SCISSORS}\ndiff --git a/x b/x\n+// ${TERM}\n`;
    expect(hook(msg('h3', text))).toBe(0);
  });

  it('ignores comment lines in the editor template, which git strips', () => {
    expect(hook(msg('h4', `fix: something\n\n${HELP}\n#\tmodified:   ${TERM}.go\n`))).toBe(0);
  });

  it('scans a # line and a scissors line given with -m, where git keeps them', () => {
    expect(hook(msg('h5', `fix: something\n\n# ${TERM}\n`))).toBe(1);
    expect(hook(msg('h6', `fix: something\n\n${SCISSORS}\n${TERM}\n`))).toBe(1);
  });

  it('checks the lines above the scissors line', () => {
    expect(hook(msg('h7', `fix: ${TERM}\n\n${HELP}\n${SCISSORS}\ndiff\n`))).toBe(1);
  });

  it('follows commit.cleanup', () => {
    git(repo, 'config', 'commit.cleanup', 'strip');
    expect(hook(msg('h8', `fix: something\n\n# ${TERM}\n`))).toBe(0);
    git(repo, 'config', 'commit.cleanup', 'scissors');
    expect(hook(msg('h9', `fix: something\n\n# ${TERM}\n${SCISSORS}\n${TERM}\n`))).toBe(1);
    expect(hook(msg('h10', `fix: something\n\n${SCISSORS}\n${TERM}\n`))).toBe(0);
    git(repo, 'config', 'commit.cleanup', 'verbatim');
    expect(hook(msg('h11', `fix: something\n\n${HELP}\n# ${TERM}\n`))).toBe(1);
    git(repo, 'config', '--unset', 'commit.cleanup');
  });

  it('respects core.commentChar', () => {
    git(repo, 'config', 'core.commentChar', ';');
    const text = `fix: something\n\n; Please enter the commit message\n; ${TERM}\n`;
    expect(hook(msg('h12', text))).toBe(0);
    git(repo, 'config', '--unset', 'core.commentChar');
  });
});

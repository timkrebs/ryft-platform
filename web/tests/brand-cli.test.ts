import { execFileSync, spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { beforeAll, describe, expect, it } from 'vitest';

// scripts/check-brand.mjs in a throwaway repository laid out like the monorepo (web/ next to
// backend/). Fixtures are ROT13, so this file never spells the name.
const rot13 = (s: string) =>
  s.replace(/[a-z]/gi, (c) => {
    const base = c <= 'Z' ? 65 : 97;
    return String.fromCharCode(((c.charCodeAt(0) - base + 13) % 26) + base);
  });
const TERM = rot13('jubbc');

let repo: string;
const git = (...args: string[]) => execFileSync('git', args, { cwd: repo, stdio: 'ignore' });
const write = (rel: string, text: string) => {
  mkdirSync(join(repo, rel, '..'), { recursive: true });
  writeFileSync(join(repo, rel), text);
};
const run = (...args: string[]) =>
  spawnSync('node', ['web/scripts/check-brand.mjs', ...args], { cwd: repo, encoding: 'utf8' });

beforeAll(() => {
  repo = mkdtempSync(join(tmpdir(), 'ryft-brand-'));
  git('init', '-q');
  for (const f of ['scripts/check-brand.mjs', 'scripts/lib/brand.mjs', 'config/brand-denylist.json']) {
    mkdirSync(join(repo, 'web', f, '..'), { recursive: true });
    cpSync(resolve(f), join(repo, 'web', f));
  }
});

describe('check-brand.mjs --staged', () => {
  it('ignores staged files outside web/', () => {
    write('backend/main.go', `// ${TERM}\n`);
    git('add', 'backend/main.go');
    const r = run('--staged');
    expect(r.status).toBe(0);
    expect(r.stdout).toContain('0 inputs');
    git('reset', '-q');
  });

  it('blocks a staged file under web/, naming it relative to web/', () => {
    write('web/src/page.ts', `// ${TERM}\n`);
    git('add', 'web/src/page.ts');
    const r = run('--staged');
    expect(r.status).toBe(1);
    expect(r.stdout).toContain('src/page.ts:1');
    expect(r.stdout).not.toContain(TERM);
    git('reset', '-q');
  });

  it('checks the staged content, not the working tree', () => {
    write('web/src/clean.ts', 'export {};\n');
    git('add', 'web/src/clean.ts');
    write('web/src/clean.ts', `// ${TERM}\n`);
    expect(run('--staged').status).toBe(0);
    git('reset', '-q');
  });

  it('blocks a file name under web/', () => {
    write(`web/src/${TERM}.ts`, 'export {};\n');
    git('add', `web/src/${TERM}.ts`);
    expect(run('--staged').status).toBe(1);
    git('reset', '-q');
  });
});

describe('check-brand.mjs --message', () => {
  it('blocks a commit message with the name and passes a clean one', () => {
    write('msg-bad', `fix: ${TERM}\n`);
    write('msg-ok', 'fix: something\n');
    expect(run('--message', join(repo, 'msg-bad')).status).toBe(1);
    expect(run('--message', join(repo, 'msg-ok')).status).toBe(0);
  });
});

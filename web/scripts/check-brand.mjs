#!/usr/bin/env node
// Brand guard: the denylisted wearable brand never appears in this repository or in
// what it builds, and comparison phrases or borrowed feature names get a warning.
// Rules: config/brand-denylist.json. Matcher: scripts/lib/brand.mjs.
//
//   node scripts/check-brand.mjs              every tracked or new file, out/ if built, the last commit message
//   node scripts/check-brand.mjs --out-only   only the built site (postbuild)
//   node scripts/check-brand.mjs --staged     the staged versions of staged files under web/ (pre-commit)
//   node scripts/check-brand.mjs --message F  a commit message file (commit-msg; the rule is repo-wide)
//
// Exit 1 on any hard finding. Matches are printed masked, so the log doesn't repeat them.
import { execFileSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { isMarkupPath, isTextPath, loadDenylist, redact, scanText } from './lib/brand.mjs';

const ROOT = resolve(import.meta.dirname, '..');
const REPO = resolve(ROOT, '..'); // web/ is a folder of the platform repository
const MAX_BYTES = 5 * 1024 * 1024;
const args = process.argv.slice(2);
const denylist = loadDenylist();

/** @type {{source: string, text: string}[]} */
const inputs = [];

if (args.includes('--message')) {
  const file = args[args.indexOf('--message') + 1];
  inputs.push({ source: 'commit message', text: messageText(readFileSync(file, 'utf8'), commentChar()) });
} else if (args.includes('--staged')) {
  // Only web/. The git calls run from the repository root (a hook in a linked worktree has
  // GIT_DIR set, which makes `--relative` from web/ list the whole repository), limited by a
  // pathspec; paths are shown relative to web/.
  const staged = git(['diff', '--cached', '--name-only', '-z', '--diff-filter=d', '--', 'web'], REPO)
    .split('\0')
    .filter(Boolean);
  for (const full of staged) {
    const path = full.replace(/^web\//, '');
    inputs.push({ source: `path ${path}`, text: path });
    if (!isTextPath(path)) continue;
    inputs.push({ source: path, text: git(['show', `:${full}`], REPO) });
  }
} else {
  if (!args.includes('--out-only')) {
    for (const path of repoFiles()) addFile(path);
    const message = lastCommitMessage();
    if (message) inputs.push({ source: 'last commit message', text: message });
  }
  const out = join(ROOT, 'out');
  if (existsSync(out)) for (const path of walk(out)) addFile(relative(ROOT, path));
  else if (args.includes('--out-only')) fail('out/ does not exist: run `npm run build` first.');
}

let hard = 0;
let warn = 0;
const annotate = !!process.env.GITHUB_ACTIONS;
for (const { source, text } of inputs) {
  const file = source.startsWith('path ') ? source.slice(5) : source;
  for (const f of scanText(text, denylist, { file, markup: isMarkupPath(file) })) {
    const where = source.startsWith('path ') ? `${file} (file name)` : `${file}:${f.line}:${f.column}`;
    const shown = f.level === 'hard' ? redact(f.context, denylist) : f.context;
    if (f.level === 'hard') hard++;
    else warn++;
    if (annotate && !source.startsWith('path ') && source.includes('/')) {
      const kind = f.level === 'hard' ? 'error' : 'warning';
      console.log(`::${kind} file=${file},line=${f.line},col=${f.column},title=brand ${f.id}::${f.note}`);
    }
    console.log(`${f.level === 'hard' ? 'ERROR' : 'warn '} ${where} [${f.id}] ${shown}`);
  }
}

const scanned = inputs.length;
if (hard) {
  console.error(
    `\nbrand check: ${hard} hard finding(s), ${warn} warning(s) in ${scanned} inputs. See config/brand-denylist.json.`,
  );
  process.exit(1);
}
console.log(`brand check: ok (${warn} warning(s), ${scanned} inputs).`);

function addFile(path) {
  inputs.push({ source: `path ${path}`, text: path });
  if (!isTextPath(path)) return;
  const abs = join(ROOT, path);
  const st = statSync(abs);
  if (!st.isFile() || st.size > MAX_BYTES) return;
  inputs.push({ source: path, text: readFileSync(abs, 'utf8') });
}

// Tracked files plus new ones that aren't ignored; without git, the source folders.
function repoFiles() {
  try {
    return git(['ls-files', '-z', '--cached', '--others', '--exclude-standard'])
      .split('\0')
      .filter((p) => p && existsSync(join(ROOT, p)) && !p.startsWith('out/'));
  } catch {
    return ['src', 'content', 'public', 'messages', 'config', 'scripts', 'docs']
      .filter((d) => existsSync(join(ROOT, d)))
      .flatMap((d) => [...walk(join(ROOT, d))].map((p) => relative(ROOT, p)));
  }
}

function lastCommitMessage() {
  try {
    return git(['log', '-1', '--format=%B']);
  } catch {
    return '';
  }
}

function* walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules' || entry.name === '.git' || entry.name === '.next') continue;
    const p = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(p);
    else yield p;
  }
}

function git(argv, cwd = ROOT) {
  return execFileSync('git', argv, {
    cwd,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    stdio: ['ignore', 'pipe', 'ignore'],
  });
}

function commentChar() {
  try {
    const c = git(['config', '--get', 'core.commentChar'], REPO).trim();
    return c.length === 1 ? c : '#';
  } catch {
    return '#';
  }
}

/**
 * What git will record from a commit message file: everything below the scissors line
 * (`git commit -v` puts the diff there) is cut always; comment lines are blanked only
 * when git will strip them, i.e. when the file is the editor's template (it carries the
 * "Please enter the commit message" help). With -m a `#` line is part of the message.
 */
function messageText(raw, cc = '#') {
  const lines = raw.split('\n');
  const scissors = lines.findIndex((l) => l === `${cc} ------------------------ >8 ------------------------`);
  const kept = scissors === -1 ? lines : lines.slice(0, scissors);
  const editor = kept.some((l) => l.startsWith(`${cc} Please enter the commit message`));
  return kept.map((l) => (editor && l.startsWith(cc) ? '' : l)).join('\n');
}

function fail(message) {
  console.error(message);
  process.exit(1);
}

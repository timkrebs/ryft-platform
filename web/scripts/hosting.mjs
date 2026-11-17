#!/usr/bin/env node
// The site as Firebase Hosting serves it, locally (web D19): the Hosting emulator of the pinned
// firebase-tools (.firebase-tools-version, the one place the version lives) on out/ with the
// generated firebase.json, at http://localhost:8080. No login: the project is a demo- one.
//
// The CLI lives in its own prefix, node_modules/.cache/firebase-tools-<version> (so `npm ci` clears
// it), installed with npm's install scripts off, as the deploy runs it. One package then gets its
// install script: re2, the native RE2 that the emulator's static server (superstatic) loads for
// firebase.json's `regex` rules. Without it superstatic falls back to JavaScript's RegExp, which
// cannot parse RE2's (?P<name>…), and the emulator dies on the first request (C1's first CI run).
// With it the emulator matches with RE2, as Hosting does.
//
//   node scripts/hosting.mjs exec     the emulator around `checks` (npm run e2e:hosting, make web-e2e)
//   node scripts/hosting.mjs serve    the emulator until Ctrl-C (npm run hosting, make web-hosting)
//   node scripts/hosting.mjs checks   Playwright, check-hosting.mjs (with --emulator: the emulator's
//                                     expectations, web D21), links and Lighthouse against the
//                                     running emulator: all four run, exit 1 if any failed
//   node scripts/hosting.mjs version  the pinned firebase-tools version
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { join, relative, resolve } from 'node:path';

const ROOT = resolve(import.meta.dirname, '..');
const BASE = 'http://localhost:8080';
const PROJECT = 'demo-ryft';
const NEEDS = ['out/index.html', 'out/.well-known/apple-app-site-association', 'firebase.json'];

const cmd = process.argv[2];
if (cmd === 'version') {
  console.log(version());
} else if (cmd === 'exec' || cmd === 'serve') {
  for (const f of NEEDS) {
    if (!existsSync(join(ROOT, f))) fail(`hosting: ${f} is missing; run \`npm run build\` first.`);
  }
  const firebase = cli();
  const only = ['--only', 'hosting', '--project', PROJECT];
  const checks = 'node scripts/hosting.mjs checks';
  const args = cmd === 'exec' ? ['emulators:exec', ...only, checks] : ['emulators:start', ...only];
  const code = run(firebase, args, { ...process.env, NO_UPDATE_NOTIFIER: '1' });
  if (code !== 0) debugLog();
  process.exit(code);
} else if (cmd === 'checks') {
  const steps = [
    ['Playwright', 'npx', ['--no', 'playwright', 'test']],
    ['check-hosting', process.execPath, ['scripts/check-hosting.mjs', '--base', BASE, '--emulator']],
    ['links', 'npm', ['run', 'links']],
    ['Lighthouse', 'npm', ['run', 'lighthouse']],
  ];
  const failed = [];
  for (const [name, bin, args] of steps) {
    console.log(`\nhosting: ${name}`);
    if (run(bin, args, process.env) !== 0) failed.push(name);
  }
  if (failed.length) fail(`\nhosting: failed: ${failed.join(', ')}.`);
  console.log('\nhosting: Playwright, check-hosting, links and Lighthouse passed.');
} else {
  fail('usage: node scripts/hosting.mjs exec|serve|checks|version');
}

function version() {
  const v = readFileSync(join(ROOT, '.firebase-tools-version'), 'utf8').trim();
  if (!/^\d+\.\d+\.\d+$/.test(v)) {
    fail(`hosting: .firebase-tools-version must hold one exact version, not ${JSON.stringify(v)}.`);
  }
  return v;
}

// The pinned CLI's bin, installed on first use: the whole tree without install scripts, then re2's
// alone (npm rebuild re2), and re2 must load from where superstatic requires it.
function cli() {
  const v = version();
  const dir = join(ROOT, 'node_modules', '.cache', `firebase-tools-${v}`);
  const bin = join(dir, 'node_modules', '.bin', 'firebase');
  const where = relative(ROOT, dir);
  const npm = (args, scripts) => {
    const env = {
      ...process.env,
      npm_config_ignore_scripts: scripts ? 'false' : 'true',
      npm_config_update_notifier: 'false',
    };
    return run('npm', [...args, '--prefix', dir, '--no-audit', '--no-fund'], env);
  };
  if (!existsSync(bin)) {
    console.log(`hosting: installing firebase-tools ${v} into ${where}, install scripts off`);
    mkdirSync(dir, { recursive: true });
    writeFileSync(join(dir, 'package.json'), '{ "private": true }\n');
    if (npm(['install', '--save-exact', `firebase-tools@${v}`], false) !== 0 || !existsSync(bin)) {
      fail(`hosting: npm could not install firebase-tools ${v} into ${where}.`);
    }
  }
  if (!re2Loads(dir)) {
    console.log("hosting: building re2 (its install script alone), for firebase.json's regex rules");
    npm(['rebuild', 're2'], true);
    if (!re2Loads(dir)) {
      console.error(`hosting: re2 does not load in ${where}, so the emulator would die on (?P<name>...).`);
      fail('hosting: delete that folder and run again (re2 needs its prebuilt binary or node-gyp).');
    }
  }
  return bin;
}

// re2 as superstatic resolves it (its own node_modules, then the prefix's).
function re2Loads(dir) {
  try {
    createRequire(join(dir, 'node_modules', 'superstatic', 'package.json'))('re2');
    return true;
  } catch {
    return false;
  }
}

// The emulator's own log, when it failed: why it stopped is there, not on stdout.
function debugLog() {
  const log = join(ROOT, 'firebase-debug.log');
  if (!existsSync(log)) return;
  const lines = readFileSync(log, 'utf8').trimEnd().split('\n');
  console.error(`\nhosting: the last ${Math.min(lines.length, 80)} lines of firebase-debug.log:`);
  console.error(lines.slice(-80).join('\n'));
}

function run(bin, args, env) {
  const r = spawnSync(bin, args, { cwd: ROOT, stdio: 'inherit', env });
  if (r.error) console.error(`hosting: ${bin}: ${r.error.message}`);
  return r.status ?? 1;
}

function fail(message) {
  console.error(message);
  process.exit(1);
}

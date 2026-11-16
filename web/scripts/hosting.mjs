#!/usr/bin/env node
// The site as Firebase Hosting serves it, locally (web D19): the Hosting emulator of the pinned
// firebase-tools (.firebase-tools-version, the one place the version lives) on out/ with the
// generated firebase.json, at http://localhost:8080. No login: the project is a demo- one. npm's
// install scripts stay off (npm_config_ignore_scripts=true), as in the deploy, so a passing run
// proves the CLI works without them; npm_config_ignore_scripts=false in the environment turns them
// back on.
//
//   node scripts/hosting.mjs exec     the emulator around `checks` (npm run e2e:hosting, make web-e2e)
//   node scripts/hosting.mjs serve    the emulator until Ctrl-C (npm run hosting, make web-hosting)
//   node scripts/hosting.mjs checks   Playwright, check-hosting.mjs, links and Lighthouse against
//                                     the running emulator: all four run, exit 1 if any failed
//   node scripts/hosting.mjs version  the pinned firebase-tools version
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

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
  const args = ['--yes', `firebase-tools@${version()}`];
  const only = ['--only', 'hosting', '--project', PROJECT];
  if (cmd === 'exec') args.push('emulators:exec', ...only, 'node scripts/hosting.mjs checks');
  else args.push('emulators:start', ...only);
  process.exit(run('npx', args, cliEnv()));
} else if (cmd === 'checks') {
  const steps = [
    ['Playwright', 'npx', ['--no', 'playwright', 'test']],
    ['check-hosting', process.execPath, ['scripts/check-hosting.mjs', '--base', BASE]],
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

// The CLI's environment: install scripts off unless set explicitly, no update check.
function cliEnv() {
  return {
    ...process.env,
    npm_config_ignore_scripts: process.env.npm_config_ignore_scripts || 'true',
    NO_UPDATE_NOTIFIER: '1',
  };
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

#!/usr/bin/env node
// Builds and runs the nginx image locally with Docker or Podman (whichever exists;
// CONTAINER_CLI overrides), the way the cluster runs it: read-only root filesystem,
// writable /tmp and /var/cache/nginx, non-root, all capabilities dropped.
//
//   node scripts/container.mjs build        → image ryft-web:local
//   node scripts/container.mjs run [port]   → http://localhost:<port> (default 8080), detached
//   node scripts/container.mjs stop
import { execFileSync, spawnSync } from 'node:child_process';
import { resolve } from 'node:path';

const ROOT = resolve(import.meta.dirname, '..');
const IMAGE = process.env.IMAGE ?? 'ryft-web:local';
const NAME = 'ryft-web-local';
const cli = process.env.CONTAINER_CLI ?? (has('docker') ? 'docker' : has('podman') ? 'podman' : null);
if (!cli) {
  console.error('container: neither docker nor podman found (set CONTAINER_CLI).');
  process.exit(1);
}

const [cmd = 'build', port = '8080'] = process.argv.slice(2);
if (cmd === 'build') {
  const args = ['build', '-t', IMAGE];
  for (const key of ['NEXT_PUBLIC_TESTFLIGHT_URL', 'NEXT_PUBLIC_SUPPORT_EMAIL']) {
    if (process.env[key]) args.push('--build-arg', `${key}=${process.env[key]}`);
  }
  run([...args, '.']);
} else if (cmd === 'run') {
  spawnSync(cli, ['rm', '-f', NAME], { stdio: 'ignore' });
  run([
    'run',
    '-d',
    '--name',
    NAME,
    '-p',
    `${port}:8080`,
    '--read-only',
    '--tmpfs',
    '/tmp',
    '--tmpfs',
    '/var/cache/nginx',
    '--cap-drop',
    'ALL',
    '--security-opt',
    'no-new-privileges',
    '--user',
    '101:101',
    IMAGE,
  ]);
  console.log(`container: ${IMAGE} on http://localhost:${port}`);
} else if (cmd === 'stop') {
  run(['rm', '-f', NAME]);
} else {
  console.error(`container: unknown command ${cmd}`);
  process.exit(1);
}

function run(args) {
  const r = spawnSync(cli, args, { cwd: ROOT, stdio: 'inherit' });
  if (r.status !== 0) process.exit(r.status ?? 1);
}

function has(bin) {
  try {
    execFileSync(bin, ['--version'], { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

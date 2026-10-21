#!/usr/bin/env node
// The data behind /licenses, generated at build time into src/generated/licenses.json:
//   app  — the Swift packages the iOS app ships (config/app-packages.json, a snapshot of
//          ../app's Package.resolved), each licence fetched from GitHub at its pinned
//          revision, plus the Satoshi typeface;
//   web  — every production npm dependency of this site (npm query .prod) with the
//          licence text from its package, plus Satoshi.
//
//   node scripts/licenses.mjs              generate (fetches missing texts into .cache/licenses)
//   node scripts/licenses.mjs --if-missing only when src/generated/licenses.json is absent
//   node scripts/licenses.mjs --sync       refresh config/app-packages.json from ../app
//   node scripts/licenses.mjs --check      exit 1 when the snapshot differs from ../app (CI)
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const ROOT = resolve(import.meta.dirname, '..');
const APP = resolve(ROOT, process.env.RYFT_APP_DIR ?? '../app');
const RESOLVED = join(APP, 'Ryft.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved');
const SNAPSHOT = join(ROOT, 'config/app-packages.json');
const CACHE = join(ROOT, '.cache/licenses');
const OUT = join(ROOT, 'src/generated/licenses.json');
const FONT_LICENSE = join(ROOT, 'src/fonts/FFL.txt');
const args = process.argv.slice(2);

if (args.includes('--if-missing') && existsSync(OUT)) process.exit(0);

if (args.includes('--sync') || args.includes('--check')) {
  if (!existsSync(RESOLVED)) {
    if (args.includes('--check') && !process.env.CI) {
      console.warn(`licenses: ${RESOLVED} not found; skipping the drift check outside CI.`);
      process.exit(0);
    }
    console.error(`licenses: ${RESOLVED} not found (set RYFT_APP_DIR).`);
    process.exit(1);
  }
  const pins = JSON.parse(readFileSync(RESOLVED, 'utf8')).pins.map((p) => ({
    identity: p.identity,
    location: p.location,
    version: p.state.version ?? null,
    revision: p.state.revision,
  }));
  const snapshot = `${JSON.stringify({ $comment: 'Snapshot of ../app Package.resolved pins. Refresh: npm run licenses -- --sync', pins }, null, 2)}\n`;
  if (args.includes('--check')) {
    const current = existsSync(SNAPSHOT) ? readFileSync(SNAPSHOT, 'utf8') : '';
    if (current !== snapshot) {
      console.error(
        'licenses: config/app-packages.json is out of date with the app. Run `npm run licenses -- --sync`.',
      );
      process.exit(1);
    }
    console.log(`licenses: app snapshot up to date (${pins.length} Swift packages).`);
    process.exit(0);
  }
  writeFileSync(SNAPSHOT, snapshot);
  console.log(`licenses: config/app-packages.json updated (${pins.length} Swift packages).`);
  process.exit(0);
}

mkdirSync(CACHE, { recursive: true });
mkdirSync(join(ROOT, 'src/generated'), { recursive: true });

const satoshi = {
  name: 'Satoshi',
  version: 'Fontshare',
  license: 'ITF Free Font License',
  holder: 'Indian Type Foundry',
  url: 'https://www.fontshare.com/fonts/satoshi',
  text: existsSync(FONT_LICENSE)
    ? readFileSync(FONT_LICENSE, 'utf8').trim()
    : fail('src/fonts/FFL.txt missing: run `npm run fonts`'),
};

const app = [];
for (const pin of JSON.parse(readFileSync(SNAPSHOT, 'utf8')).pins) {
  const repo = pin.location.replace(/^https:\/\/github\.com\//, '').replace(/\.git$/, '');
  const text = await swiftLicense(pin.identity, repo, pin.revision);
  app.push({
    name: pin.identity,
    version: pin.version ?? pin.revision.slice(0, 7),
    license: identify(text),
    url: `https://github.com/${repo}/tree/${pin.revision}`,
    text,
  });
}
app.push(satoshi);

const pkg = JSON.parse(readFileSync(join(ROOT, 'package.json'), 'utf8'));
const devOnly = new Set(Object.keys(pkg.devDependencies ?? {}));
const nodes = JSON.parse(
  execFileSync('npm', ['query', '.prod'], { cwd: ROOT, encoding: 'utf8', maxBuffer: 64 << 20 }),
);
const seen = new Set();
const web = [];
for (const n of nodes.sort((a, b) => a.name.localeCompare(b.name) || a.version.localeCompare(b.version))) {
  const key = `${n.name}@${n.version}`;
  // `location` is relative to the project (npm masks UUID-like segments in absolute paths).
  // Optional dependencies for other platforms appear in the tree without being installed.
  const dir = join(ROOT, n.location ?? '');
  if (!n.location || devOnly.has(n.name) || seen.has(key) || !existsSync(dir)) continue;
  seen.add(key);
  const file = readdirSync(dir).find((f) => /^(licen[cs]e|copying)(\.|-|$)/i.test(f));
  web.push({
    name: n.name,
    version: n.version,
    license: typeof n.license === 'string' ? n.license : (n.license?.type ?? 'see text'),
    url: repoUrl(n),
    text: file ? readFileSync(join(dir, file), 'utf8').trim() : null,
  });
}
web.push(satoshi);

writeFileSync(OUT, `${JSON.stringify({ app, web }, null, 1)}\n`);
const missing = web.filter((w) => !w.text).length;
console.log(
  `licenses: ${app.length} app and ${web.length} website components (${missing} without a licence file).`,
);

async function swiftLicense(identity, repo, revision) {
  const cached = join(CACHE, `${identity}@${revision}.txt`);
  if (existsSync(cached)) return readFileSync(cached, 'utf8');
  for (const name of ['LICENSE', 'LICENSE.md', 'LICENSE.txt', 'LICENCE', 'COPYING']) {
    const res = await fetch(`https://raw.githubusercontent.com/${repo}/${revision}/${name}`);
    if (res.ok) {
      const text = (await res.text()).trim();
      writeFileSync(cached, text);
      return text;
    }
  }
  return fail(`no licence file found for ${repo} at ${revision}`);
}

function identify(text) {
  if (/Apache License\s+Version 2\.0/i.test(text)) return 'Apache-2.0';
  if (/Permission is hereby granted, free of charge/i.test(text)) return 'MIT';
  if (/Redistribution and use in source and binary forms/i.test(text)) return 'BSD';
  return 'see text';
}

function repoUrl(n) {
  const raw = typeof n.repository === 'string' ? n.repository : n.repository?.url;
  if (raw) {
    const url = raw
      .replace(/^git\+/, '')
      .replace(/\.git$/, '')
      .replace(/^git:\/\//, 'https://');
    if (/^github:/.test(url)) return `https://github.com/${url.slice(7)}`;
    if (/^[\w-]+\/[\w.-]+$/.test(url)) return `https://github.com/${url}`;
    if (/^https?:\/\//.test(url)) return url;
  }
  return n.homepage ?? `https://www.npmjs.com/package/${n.name}`;
}

function fail(message) {
  console.error(`licenses: ${message}`);
  process.exit(1);
}

#!/usr/bin/env node
// Satoshi from Fontshare (ITF Free Font License, the same files the app uses). The
// licence allows self-hosting on our own site but not redistributing the files through
// a repository, so they are git-ignored and fetched here, at build time. The licence also
// forbids subsetting and format conversion: the WOFF2 files are used exactly as supplied,
// and each one is pinned by its SHA-256.
//
//   node scripts/fetch-fonts.mjs              fetch (always)
//   node scripts/fetch-fonts.mjs --if-missing fetch only when a file is missing or differs
//
// When Fontshare publishes new files the hashes stop matching: read the licence again,
// then update config/fonts.json.
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { inflateRawSync } from 'node:zlib';

const ROOT = resolve(import.meta.dirname, '..');
const CONFIG = JSON.parse(readFileSync(join(ROOT, 'config/fonts.json'), 'utf8'));
const URL = CONFIG.url;
const DEST = join(ROOT, 'src/fonts');
/** File in the zip → [file name here, SHA-256]. */
const PINS = Object.fromEntries(
  Object.entries(CONFIG.files).map(([entry, f]) => [entry, [f.name, f.sha256]]),
);

const sha256 = (buf) => createHash('sha256').update(buf).digest('hex');
const present = () =>
  Object.values(PINS).every(([name, hash]) => {
    const p = join(DEST, name);
    return existsSync(p) && sha256(readFileSync(p)) === hash;
  });

if (process.argv.includes('--if-missing') && present()) process.exit(0);

console.log(`fonts: downloading Satoshi from ${URL}`);
const res = await fetch(URL);
if (!res.ok) {
  console.error(`fonts: download failed: HTTP ${res.status}`);
  process.exit(1);
}
const zip = Buffer.from(await res.arrayBuffer());
const files = unzip(zip, Object.keys(PINS));
mkdirSync(DEST, { recursive: true });
for (const [entry, [name, hash]] of Object.entries(PINS)) {
  const data = files.get(entry);
  if (!data) fail(`${entry} is missing from the download`);
  const actual = sha256(data);
  if (actual !== hash)
    fail(`${entry} changed upstream (sha256 ${actual}); check the licence, then update config/fonts.json`);
  writeFileSync(join(DEST, name), data);
}
console.log(`fonts: Satoshi Regular, Medium, Bold and the licence in src/fonts/ (verified).`);

function fail(message) {
  console.error(`fonts: ${message}`);
  process.exit(1);
}

// A minimal reader for the zip's central directory: stored and deflated entries only.
function unzip(buf, wanted) {
  const out = new Map();
  let eocd = -1;
  for (let i = buf.length - 22; i >= Math.max(0, buf.length - 65557); i--) {
    if (buf.readUInt32LE(i) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) fail('the download is not a zip file');
  const count = buf.readUInt16LE(eocd + 10);
  let p = buf.readUInt32LE(eocd + 16);
  for (let n = 0; n < count; n++) {
    if (buf.readUInt32LE(p) !== 0x02014b50) fail('unexpected zip layout');
    const method = buf.readUInt16LE(p + 10);
    const compressed = buf.readUInt32LE(p + 20);
    const nameLen = buf.readUInt16LE(p + 28);
    const extraLen = buf.readUInt16LE(p + 30);
    const commentLen = buf.readUInt16LE(p + 32);
    const local = buf.readUInt32LE(p + 42);
    const name = buf.subarray(p + 46, p + 46 + nameLen).toString('utf8');
    p += 46 + nameLen + extraLen + commentLen;
    if (!wanted.includes(name)) continue;
    const start = local + 30 + buf.readUInt16LE(local + 26) + buf.readUInt16LE(local + 28);
    const raw = buf.subarray(start, start + compressed);
    if (method === 0) out.set(name, Buffer.from(raw));
    else if (method === 8) out.set(name, inflateRawSync(raw));
    else fail(`${name} uses zip method ${method}`);
  }
  return out;
}

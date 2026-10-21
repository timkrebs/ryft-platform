#!/usr/bin/env node
// Gzip every text file in out/ over 1 KB next to the original (*.gz), for nginx's
// gzip_static. Run in the image build; the AASA file stays as it is.
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { constants, gzipSync } from 'node:zlib';

const OUT = resolve(import.meta.dirname, '../out');
const TEXT = /\.(html|css|js|json|svg|xml|txt|webmanifest)$/;
let count = 0;
for (const file of walk(OUT)) {
  const rel = relative(OUT, file);
  if (!TEXT.test(rel) || rel.startsWith('.well-known/') || statSync(file).size < 1024) continue;
  writeFileSync(`${file}.gz`, gzipSync(readFileSync(file), { level: constants.Z_BEST_COMPRESSION }));
  count++;
}
console.log(`precompress: ${count} files gzipped.`);

function* walk(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* walk(p);
    else yield p;
  }
}

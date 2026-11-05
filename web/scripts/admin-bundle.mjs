#!/usr/bin/env node
// prebuild: bundle src/admin/main.ts into src/generated/admin.js (the /admin page inlines it).
import { mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { bundleAdmin } from './lib/admin-bundle.mjs';

const OUT = resolve(import.meta.dirname, '../src/generated/admin.js');
const text = await bundleAdmin();
mkdirSync(resolve(OUT, '..'), { recursive: true });
writeFileSync(OUT, text);
console.log(`admin-bundle: ${(text.length / 1024).toFixed(1)} KB → src/generated/admin.js`);

// The admin page's script: src/admin/main.ts bundled by esbuild into one minified IIFE that
// is inlined into /admin (web D15). esbuild is a dev dependency; nothing of it ships.
import { build } from 'esbuild';
import { resolve } from 'node:path';

const ROOT = resolve(import.meta.dirname, '../..');

/** The bundle as text. Throws when the text could not sit safely inside a <script> element. */
export async function bundleAdmin() {
  const result = await build({
    entryPoints: [resolve(ROOT, 'src/admin/main.ts')],
    bundle: true,
    format: 'iife',
    platform: 'browser',
    target: 'es2022',
    charset: 'utf8',
    minify: true,
    legalComments: 'none',
    write: false,
    logLevel: 'warning',
    tsconfig: resolve(ROOT, 'tsconfig.json'),
  });
  const text = result.outputFiles[0].text.trimEnd();
  // Inline script text must not be able to end its element or start an HTML comment, and the
  // page runs under Trusted Types 'none' and without 'unsafe-eval'.
  for (const [what, re] of [
    ['a closing script tag', /<\/script/i],
    ['an HTML comment opener', /<!--/],
    ['eval()', /\beval\s*\(/],
    ['new Function', /\bnew\s+Function\b/],
    ['innerHTML', /\binnerHTML\b/],
  ]) {
    if (re.test(text)) throw new Error(`admin bundle contains ${what}`);
  }
  return text;
}

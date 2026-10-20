import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// Pages ship as static HTML without the Next runtime (docs/DECISIONS.md, D2).
function* files(dir: string): Generator<string> {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* files(p);
    else yield p;
  }
}

describe('source', () => {
  it('has no client components (they would never hydrate)', () => {
    const offenders = [...files('src')]
      .filter((f) => /\.(t|j)sx?$/.test(f))
      .filter((f) => /^\s*['"]use client['"]/m.test(readFileSync(f, 'utf8')));
    expect(offenders).toEqual([]);
  });

  it('sets no inline style attributes (the CSP allows styles only from files)', () => {
    const offenders = [...files('src')]
      .filter((f) => f.endsWith('.tsx'))
      .filter((f) => /\sstyle=\{/.test(readFileSync(f, 'utf8')));
    expect(offenders).toEqual([]);
  });

  it('hard-codes no colours outside the generated tokens', () => {
    const offenders = [...files('src')]
      .filter((f) => /\.(css|tsx?)$/.test(f) && !/src\/styles\/(tokens|swatches)\./.test(f))
      .flatMap((f) =>
        readFileSync(f, 'utf8')
          .split('\n')
          .map((line, i) => [f, i + 1, line] as const)
          .filter(([, , line]) => /#[0-9a-f]{3,8}\b(?![\w-])|\brgba?\(|\bhsla?\(/i.test(line))
          .filter(([, , line]) => !/^\s*(\/\/|\*|\/\*)/.test(line)),
      )
      .map(([f, n]) => `${f}:${n}`);
    expect(offenders).toEqual([]);
  });
});

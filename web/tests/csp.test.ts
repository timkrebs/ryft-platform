import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { hashesOf, nginxSnippet, policy } from '../scripts/lib/csp.mjs';
import { cspHash, inlineScripts, stripRuntime } from '../scripts/lib/html.mjs';
import { THEME_INIT, THEME_SWITCH } from '../src/lib/inline-scripts';

const page = `<!DOCTYPE html><html><head>
<script data-ryft="theme-init">${THEME_INIT}</script>
<script src="/_next/static/chunks/a.js" async=""></script>
<link rel="preload" as="script" fetchPriority="low" href="/_next/static/chunks/b.js"/>
</head><body><p>Hi</p>
<script>self.__next_f=self.__next_f||[]</script>
<script data-ryft="theme-switch">${THEME_SWITCH}</script>
</body></html>`;

describe('postbuild and CSP', () => {
  it('removes the Next runtime and keeps only our scripts', () => {
    const { html, removed } = stripRuntime(page);
    expect(removed).toBe(3);
    expect(html).not.toContain('__next_f');
    expect(html).not.toContain('/_next/static/chunks/');
    expect(inlineScripts(html).map((s) => s.body)).toEqual([THEME_INIT, THEME_SWITCH]);
  });

  it('hashes a script body the way browsers do (SHA-256, base64, exact bytes)', () => {
    const expected = createHash('sha256').update('alert(1)', 'utf8').digest('base64');
    expect(cspHash('alert(1)')).toBe(`'sha256-${expected}'`);
    expect(cspHash('alert(1) ')).not.toBe(cspHash('alert(1)'));
  });

  it('collects one hash per distinct script', () => {
    expect(hashesOf([THEME_INIT, THEME_SWITCH, THEME_INIT])).toHaveLength(2);
  });

  it('allows no inline script without a hash and nothing from other origins', () => {
    const p = policy(hashesOf([THEME_INIT]));
    expect(p).toContain("script-src 'sha256-");
    expect(p).not.toContain('unsafe-inline');
    expect(p).not.toContain('unsafe-eval');
    expect(p).toContain("style-src 'self'");
    expect(p).toContain("frame-ancestors 'none'");
    expect(policy([])).toContain("script-src 'none'");
  });

  it('writes an nginx directive that sends the header on every status', () => {
    const snippet = nginxSnippet(hashesOf([THEME_INIT]), 1);
    expect(snippet).toMatch(/^add_header Content-Security-Policy ".*" always;$/m);
    expect(snippet.split('"')).toHaveLength(3);
  });
});

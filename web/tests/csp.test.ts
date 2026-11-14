import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import config from '../config/admin.json';
import {
  adminNginxSnippet,
  adminPolicy,
  hashesOf,
  httpsOrigin,
  nginxSnippet,
  policy,
} from '../scripts/lib/csp.mjs';
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

// The admin page has its own policy (web D15). The marketing pages keep connect-src 'none'
// and must not learn the admin script's hash or the Supabase host.
describe('the two policies', () => {
  const ADMIN_JS = 'document.title="admin"';
  const marketing = policy(hashesOf([THEME_INIT, THEME_SWITCH]));
  const admin = adminPolicy(hashesOf([ADMIN_JS]), config);
  const directive = (p: string, name: string) =>
    p
      .split('; ')
      .find((d) => d.startsWith(`${name} `))
      ?.slice(name.length + 1);

  it('keeps connect-src none, no data: and no admin hash or origin on the marketing pages', () => {
    expect(directive(marketing, 'connect-src')).toBe("'none'");
    expect(directive(marketing, 'img-src')).toBe("'self'");
    expect(marketing).not.toContain(cspHash(ADMIN_JS));
    expect(marketing).not.toContain('supabase');
    expect(marketing).not.toContain('api.ryftlab.de');
    expect(marketing).not.toContain('data:');
    expect(directive(marketing, 'trusted-types')).toBeUndefined();
  });

  it('gives the admin page exactly the api and the Supabase project to talk to', () => {
    expect(directive(admin, 'connect-src')).toBe(`${config.supabaseUrl}/auth/v1/ ${config.apiUrl}/v1/admin/`);
    expect(directive(admin, 'script-src')).toBe(cspHash(ADMIN_JS));
    expect(directive(admin, 'img-src')).toBe("'self' data:");
    expect(directive(admin, 'trusted-types')).toBe("'none'");
    expect(directive(admin, 'require-trusted-types-for')).toBe("'script'");
    expect(directive(admin, 'frame-ancestors')).toBe("'none'");
    expect(directive(admin, 'form-action')).toBe("'none'");
    expect(directive(admin, 'default-src')).toBe("'none'");
    expect(directive(admin, 'base-uri')).toBe("'none'");
  });

  it('scopes connect-src to paths: the Auth API and the admin routes, not the whole origins', () => {
    const sources = directive(admin, 'connect-src')!.split(' ');
    expect(sources).toHaveLength(2);
    for (const s of sources) expect(new URL(s).pathname, s).toMatch(/^\/[a-z0-9/]+\/$/);
    expect(sources).not.toContain(config.apiUrl);
    expect(sources).not.toContain(config.supabaseUrl);
    expect(directive(admin, 'connect-src')).not.toMatch(/rest\/v1|storage|realtime|v1\/me/);
  });

  it("allows no unsafe source, wildcard, 'self' or strict-dynamic for scripts or connections", () => {
    for (const name of ['script-src', 'connect-src']) {
      const value = directive(admin, name)!;
      expect(value, name).not.toMatch(/unsafe-|\*|'self'|strict-dynamic|wss?:|https?:\/\/\*/);
    }
    expect(admin).not.toContain('unsafe-');
  });

  it('writes one header per snippet, never two policies in one', () => {
    for (const snippet of [nginxSnippet(["'sha256-x'"], 3), adminNginxSnippet(["'sha256-x'"], config)]) {
      expect(snippet.match(/add_header Content-Security-Policy/g)).toHaveLength(1);
      expect(snippet.split('"')).toHaveLength(3);
    }
  });

  it('accepts only bare https origins from config/admin.json', () => {
    expect(httpsOrigin(config.apiUrl, 'api')).toBe(config.apiUrl);
    expect(httpsOrigin(config.supabaseUrl, 'supabase')).toBe(config.supabaseUrl);
    for (const bad of [
      'http://api.ryftlab.de',
      'https://api.ryftlab.de/',
      'https://api.ryftlab.de/v1',
      'https://user@api.ryftlab.de',
      'https://*.supabase.co',
      'api.ryftlab.de',
      '',
    ]) {
      expect(() => httpsOrigin(bad, 'x'), bad).toThrow();
    }
    expect(() => adminPolicy([], { ...config, supabaseUrl: 'http://x.supabase.co' })).toThrow();
  });

  it('uses the same values the bundle is built from', () => {
    expect(new URL(config.apiUrl).origin).toBe(config.apiUrl);
    expect(config.supabaseKey).toMatch(/^sb_publishable_/);
    expect(config.supabaseUrl).toMatch(/^https:\/\/[a-z0-9]{20}\.supabase\.co$/);
  });
});

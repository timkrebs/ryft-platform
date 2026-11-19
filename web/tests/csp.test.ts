import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import config from '../config/admin.json';
import { adminPolicy, hashesOf, httpsOrigin, policy } from '../scripts/lib/csp.mjs';
import { cspHash, inlineScripts, stripRuntime } from '../scripts/lib/html.mjs';

// The admin bundle stands in for "our" script: since web D20 it is the only one the site has.
const ADMIN_JS = 'document.title="admin"';
const page = `<!DOCTYPE html><html><head>
<script src="/_next/static/chunks/a.js" async=""></script>
<link rel="preload" as="script" fetchPriority="low" href="/_next/static/chunks/b.js"/>
</head><body><p>Hi</p>
<script>self.__next_f=self.__next_f||[]</script>
<script data-ryft="admin">${ADMIN_JS}</script>
</body></html>`;

describe('postbuild and CSP', () => {
  it('removes the Next runtime and keeps only our scripts', () => {
    const { html, removed } = stripRuntime(page);
    expect(removed).toBe(3);
    expect(html).not.toContain('__next_f');
    expect(html).not.toContain('/_next/static/chunks/');
    expect(inlineScripts(html).map((s) => s.body)).toEqual([ADMIN_JS]);
  });

  it('leaves a page without our scripts with no script at all', () => {
    const { html } = stripRuntime(page.replace(/<script data-ryft[\s\S]*?<\/script>/, ''));
    expect(html).not.toMatch(/<script\b/);
  });

  it('hashes a script body the way browsers do (SHA-256, base64, exact bytes)', () => {
    const expected = createHash('sha256').update('alert(1)', 'utf8').digest('base64');
    expect(cspHash('alert(1)')).toBe(`'sha256-${expected}'`);
    expect(cspHash('alert(1) ')).not.toBe(cspHash('alert(1)'));
  });

  it('collects one hash per distinct script', () => {
    expect(hashesOf(['a()', 'b()', 'a()'])).toHaveLength(2);
  });

  it("allows no script on the marketing pages (script-src 'none', web D20) and nothing from other origins", () => {
    const p = policy([]);
    expect(p).toContain("script-src 'none';");
    expect(p).not.toContain('sha256-');
    expect(p).not.toContain('unsafe-inline');
    expect(p).not.toContain('unsafe-eval');
    expect(p).toContain("style-src 'self'");
    expect(p).toContain("frame-ancestors 'none'");
    // A hash, if a page ever had a script again, would be the only way in.
    expect(policy(hashesOf(['a()']))).toContain("script-src 'sha256-");
  });
});

// The admin page has its own policy (web D15). The marketing pages keep connect-src 'none'
// and must not learn the admin script's hash or the Supabase host.
describe('the two policies', () => {
  const marketing = policy([]);
  const admin = adminPolicy(hashesOf([ADMIN_JS]), config);
  const directive = (p: string, name: string) =>
    p
      .split('; ')
      .find((d) => d.startsWith(`${name} `))
      ?.slice(name.length + 1);

  it('keeps script-src and connect-src none, no data: and no admin hash or origin on the marketing pages', () => {
    expect(directive(marketing, 'script-src')).toBe("'none'");
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

  // Each is one header value (firebase.json sets each on its own rule: tests/hosting.test.ts).
  it('keeps each policy one header value: no line break, every directive once', () => {
    for (const p of [policy(["'sha256-x'"]), adminPolicy(["'sha256-x'"], config)]) {
      expect(p).not.toMatch(/[\r\n]/);
      const names = p.split('; ').map((d) => d.split(' ')[0]);
      expect(new Set(names).size).toBe(names.length);
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

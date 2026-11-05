import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

// The site's access log identifies no visitor (docs/LEGAL_CHECKLIST.md → S6): what the
// privacy policy says about ryftapp.de depends on it.
const conf = readFileSync('nginx/default.conf', 'utf8');
const format = conf.match(/^log_format site '([^']*)';$/m)?.[1];

describe('nginx access log', () => {
  it('is the site format, for the whole server', () => {
    expect(format).toBeDefined();
    expect(conf).toMatch(/^ {4}access_log \/var\/log\/nginx\/access\.log site;$/m);
    expect(conf.match(/access_log (?!off)/g)).toHaveLength(1);
  });

  it('logs no address, browser, referrer or query string', () => {
    for (const variable of [
      '$remote_addr',
      '$http_x_forwarded_for',
      '$proxy_add_x_forwarded_for',
      '$realip_remote_addr',
      '$http_user_agent',
      '$http_referer',
      '$args',
      '$query_string',
      '$request_uri',
      '$http_',
      '$cookie_',
    ]) {
      expect(format, variable).not.toContain(variable);
    }
    expect(format).not.toMatch(/\$request(?!_method|_time)/);
  });
});

// Two CSPs (web D15): nginx does not merge add_header across levels, so each location that
// sets a header includes a snippet again; exactly one of the two, and only /admin the admin one.
describe('nginx locations and the two security-header snippets', () => {
  const server = conf.slice(conf.indexOf('server {'));
  const locations = [...server.matchAll(/^ {4}location ([^{]+)\{([\s\S]*?)^ {4}\}/gm)].map((m) => ({
    name: m[1]!.trim(),
    body: m[2]!,
  }));

  it('finds the locations', () => {
    expect(locations.map((l) => l.name)).toContain('= /admin');
    expect(locations.length).toBeGreaterThan(8);
  });

  it('includes exactly one of headers.conf / headers-admin.conf wherever a location adds a header', () => {
    for (const l of locations) {
      const adds = /^\s*add_header /m.test(l.body);
      const normal = (l.body.match(/include \/etc\/nginx\/snippets\/headers\.conf;/g) ?? []).length;
      const admin = (l.body.match(/include \/etc\/nginx\/snippets\/headers-admin\.conf;/g) ?? []).length;
      expect(normal + admin, l.name).toBe(adds ? 1 : 0);
    }
  });

  it('gives only = /admin the admin policy, and the server level the marketing one', () => {
    const admins = locations.filter((l) => /headers-admin\.conf/.test(l.body)).map((l) => l.name);
    expect(admins).toEqual(['= /admin']);
    // default.conf includes no policy directly; the header snippets do.
    expect(conf).not.toMatch(/include [^;]*csp(-admin)?\.conf/);
    const outside = server.replace(/^ {4}location [\s\S]*?^ {4}\}/gm, '');
    expect(outside).toMatch(/^ {4}include \/etc\/nginx\/snippets\/headers\.conf;/m);
    expect(outside).not.toMatch(/headers-admin/);
  });

  it('never serves admin.html under the marketing policy, never caches it, keeps it out of search engines', () => {
    const admin = locations.find((l) => l.name === '= /admin')!.body;
    expect(admin).toContain('add_header Cache-Control "no-store" always;');
    expect(admin).toContain('add_header X-Robots-Tag "noindex, nofollow" always;');
    expect(admin).toContain('try_files /admin.html =404;');
    expect(locations.find((l) => l.name === '= /admin.html')!.body).toContain('return 301 /admin;');
  });

  it('answers 404 for the precompressed *.gz files, as the first regex location, without its own headers', () => {
    const names = locations.map((l) => l.name);
    const gz = names.indexOf('~ \\.gz$');
    expect(gz).toBeGreaterThan(-1);
    expect(names.slice(0, gz).filter((n) => n.startsWith('~'))).toEqual([]);
    const body = locations[gz]!.body;
    expect(body).toMatch(/return 404;/);
    // No add_header: the server-level headers (CSP included) are inherited.
    expect(body).not.toMatch(/add_header/);
    expect(conf).toMatch(/gzip_static on;/);
  });

  it('keeps the policies apart: each header file includes only its own', () => {
    const read = (f: string) => readFileSync(`nginx/${f}`, 'utf8');
    expect(read('headers.conf')).toMatch(/include \/etc\/nginx\/snippets\/csp\.conf;/);
    expect(read('headers.conf')).not.toMatch(/csp-admin/);
    expect(read('headers-admin.conf')).toMatch(/include \/etc\/nginx\/snippets\/csp-admin\.conf;/);
    expect(read('headers-admin.conf')).not.toMatch(/include \/etc\/nginx\/snippets\/csp\.conf;/);
    for (const f of ['headers.conf', 'headers-admin.conf']) {
      expect(read(f)).toMatch(/include \/etc\/nginx\/snippets\/headers-common\.conf;/);
    }
  });

  it('shares the common headers and disables neither share nor clipboard-write', () => {
    const common = readFileSync('nginx/headers-common.conf', 'utf8');
    for (const h of [
      'X-Content-Type-Options',
      'Referrer-Policy',
      'X-Frame-Options',
      'Strict-Transport-Security',
      'Cross-Origin-Opener-Policy',
      'Cross-Origin-Resource-Policy',
      'Permissions-Policy',
    ]) {
      expect(common).toContain(`add_header ${h} `);
    }
    expect(common).not.toMatch(/web-share|clipboard-write/);
    expect(common).not.toContain('Content-Security-Policy');
  });
});

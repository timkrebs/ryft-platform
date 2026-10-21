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

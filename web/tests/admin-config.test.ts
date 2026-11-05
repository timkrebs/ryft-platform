import { existsSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import config from '../config/admin.json';

// config/admin.json holds the Supabase project and the api origin the admin page talks to.
// The app ships the same project; when the app is checked out next to this repository the two
// must agree (a page pointed at the wrong project would sign in nowhere).
const APP = resolve(process.env.RYFT_APP_DIR ?? '../../app');
const XCCONFIG = join(APP, 'Config/Production.xcconfig');

describe('config/admin.json', () => {
  it('has exactly the three values the bundle and the CSP read', () => {
    expect(Object.keys(config).sort()).toEqual(['apiUrl', 'supabaseKey', 'supabaseUrl']);
  });

  it.skipIf(!existsSync(XCCONFIG))("names the app's production project and publishable key", () => {
    const x = readFileSync(XCCONFIG, 'utf8');
    const value = (name: string) =>
      new RegExp(`^${name}\\s*=\\s*(\\S+)`, 'm').exec(x)?.[1]?.replace('/$()/', '//');
    expect(value('RYFT_SUPABASE_URL')).toBe(config.supabaseUrl);
    expect(value('RYFT_SUPABASE_PUBLISHABLE_KEY')).toBe(config.supabaseKey);
  });
});

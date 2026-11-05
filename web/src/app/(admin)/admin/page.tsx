import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import config from '../../../../config/admin.json';

// out/admin.html. A server component: it renders the shell and inlines the bundle that
// scripts/admin-bundle.mjs built from src/admin (no 'use client', no React runtime ships).
export default function AdminPage() {
  const script = readFileSync(join(process.cwd(), 'src/generated/admin.js'), 'utf8');
  const apiHost = new URL(config.apiUrl).host;
  return (
    <div className="adm-shell">
      <header className="adm-top">
        <span className="adm-brand">
          RYFT <span className="adm-brand-sub">admin</span>
        </span>
        <div id="adm-session" />
      </header>
      <nav id="adm-nav" className="adm-nav" aria-label="Sections" hidden />
      <main id="adm-main" className="adm-main" tabIndex={-1}>
        <noscript>
          <p className="adm-note">
            This page needs JavaScript. It has no third-party code and sets no cookies.
          </p>
        </noscript>
      </main>
      <footer className="adm-foot">
        <p>{`Signs in with Supabase Auth; talks to ${apiHost}. Nothing else is loaded.`}</p>
      </footer>
      <div id="adm-live" className="visually-hidden" role="status" aria-live="polite" />
      <script data-ryft="admin" dangerouslySetInnerHTML={{ __html: script }} />
    </div>
  );
}

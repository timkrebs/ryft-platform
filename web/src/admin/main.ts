// Entry of the admin bundle (scripts/admin-bundle.mjs → src/generated/admin.js, inlined into /admin).
import { start } from './app';

const el = (id: string): HTMLElement => {
  const e = document.getElementById(id);
  if (!e) throw new Error(`missing #${id}`);
  return e;
};

start({ session: el('adm-session'), nav: el('adm-nav'), main: el('adm-main'), live: el('adm-live') });

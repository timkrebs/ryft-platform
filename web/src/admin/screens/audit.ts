// Audit: who called what in the last 7 days (read-only). The answer never holds an email.
import { describeError } from '../api';
import { h } from '../dom';
import { fmtTime } from '../format';
import type { AuditRecord } from '../types';
import { badge, errorBox, facts, Handled, note, type Ctx } from '../ui';

const WINDOW_DAYS = 7;

export function auditScreen(ctx: Ctx): HTMLElement {
  const list = h('div', { 'aria-busy': 'true' }, note('Loading…'));

  async function load() {
    try {
      const rows = await ctx.call(() => ctx.api.get<AuditRecord[]>('/v1/admin/audit'));
      if (rows.length === 0) {
        list.replaceChildren(note(`No admin calls in the last ${WINDOW_DAYS} days.`));
      } else {
        const ul = h('ul', { class: 'adm-cards' });
        for (const r of rows) ul.append(card(r));
        list.replaceChildren(ul);
      }
    } catch (e) {
      if (!(e instanceof Handled)) list.replaceChildren(errorBox(describeError(e)));
    } finally {
      list.setAttribute('aria-busy', 'false');
    }
  }

  function card(r: AuditRecord): HTMLElement {
    const who =
      r.actor.kind === 'user' ? `User ${(r.actor.id ?? '').slice(0, 8)}` : `Key ${r.actor.id ?? ''}`;
    // Only plain values of `details` (codes, counts, flags); nested values are left out.
    const details = Object.entries(r.details ?? {})
      .filter(
        ([, v]) =>
          (typeof v === 'string' && !v.includes('@')) || typeof v === 'number' || typeof v === 'boolean',
      )
      .map(([k, v]) => `${k}: ${String(v)}`)
      .join(', ');
    return h(
      'li',
      { class: 'adm-card' },
      h(
        'div',
        { class: 'adm-card-head' },
        h('span', { class: 'adm-title' }, r.action),
        badge(String(r.status), r.status >= 400 ? 'bad' : 'ok'),
      ),
      facts([
        ['When', fmtTime(r.at)],
        ['By', who],
        ['Account', r.target_user ? r.target_user.slice(0, 8) : null],
        ['Details', details || null],
        ['Request id', r.request_id],
      ]),
    );
  }

  const root = h(
    'div',
    {},
    h('h1', { class: 'adm-h1', id: 'adm-title', tabindex: -1 }, 'Audit'),
    note(`Admin calls of the last ${WINDOW_DAYS} days, newest first.`),
    list,
  );
  void load();
  return root;
}

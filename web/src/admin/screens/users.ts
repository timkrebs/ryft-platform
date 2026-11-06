// Users: the accounts with their beta access, grant and revoke. A token gets AdminUserBrief
// only; this screen renders exactly those fields (a test pins the set).
import { describeError } from '../api';
import { h, listen } from '../dom';
import { fmtTime, isRelay, matches } from '../format';
import type { AdminUserBrief } from '../types';
import {
  badge,
  button,
  confirmButton,
  emailToggle,
  errorBox,
  errorSlot,
  facts,
  field,
  Handled,
  input,
  note,
  refocus,
  type Ctx,
} from '../ui';

export function usersScreen(ctx: Ctx): HTMLElement {
  let users: AdminUserBrief[] = [];
  const list = h('div', { 'aria-busy': 'true' }, note('Loading…'));
  const count = h('p', { class: 'adm-note', role: 'status' });
  const filter = input('search', 'filter', {
    autocomplete: 'off',
    autocapitalize: 'none',
    spellcheck: 'false',
  });

  // Only the newest load's answer is drawn (the email switch can start a second one).
  let seq = 0;
  async function load(): Promise<void> {
    const mine = ++seq;
    list.setAttribute('aria-busy', 'true');
    try {
      const q = ctx.showEmails() ? '?emails=1' : '';
      const answer = await ctx.call(() => ctx.api.get<AdminUserBrief[]>(`/v1/admin/users${q}`));
      if (mine === seq) {
        users = answer;
        render();
      }
    } catch (e) {
      if (mine === seq && !(e instanceof Handled)) list.replaceChildren(errorBox(describeError(e)));
    } finally {
      if (mine === seq) list.setAttribute('aria-busy', 'false');
    }
  }

  function render() {
    const shown = users.filter((u) => matches(filter.value, emailOf(u), u.short_id, u.provider));
    count.textContent = `${shown.length} of ${users.length} accounts`;
    if (shown.length === 0) {
      list.replaceChildren(note(users.length === 0 ? 'No accounts yet.' : 'No account matches.'));
      return;
    }
    const ul = h('ul', { class: 'adm-cards adm-grid' });
    for (const u of shown) ul.append(card(u));
    list.replaceChildren(ul);
  }

  /** An address is shown only while the toggle is on, whatever the answer held. */
  const emailOf = (u: AdminUserBrief) => (ctx.showEmails() ? (u.email ?? null) : null);

  function card(u: AdminUserBrief): HTMLElement {
    const actions = h('div', { class: 'adm-actions' });
    const failure = errorSlot();
    // One change at a time per account: a double click must not send it twice.
    let busy = false;
    const change = async (grant: boolean, b?: HTMLButtonElement) => {
      if (busy) return;
      busy = true;
      if (b) b.disabled = true;
      failure.clear();
      try {
        await ctx.call(() =>
          grant
            ? ctx.api.post(`/v1/admin/users/${encodeURIComponent(u.id)}/access`)
            : ctx.api.del(`/v1/admin/users/${encodeURIComponent(u.id)}/access`),
        );
        ctx.announce(grant ? 'Access granted' : 'Access revoked');
        await load();
        refocus(list, u.id);
      } catch (e) {
        if (!(e instanceof Handled)) failure.show(describeError(e));
      } finally {
        busy = false;
        if (b?.isConnected) b.disabled = false;
      }
    };
    if (u.beta_access) {
      actions.append(confirmButton('Revoke access', 'Revoke now', () => change(false)));
    } else {
      actions.append(button('Grant access', 'primary', (b) => void change(true, b)));
    }
    return h(
      'li',
      { class: 'adm-card', 'data-key': u.id, tabindex: -1 },
      h(
        'div',
        { class: 'adm-card-head' },
        h('span', { class: 'adm-title' }, emailOf(u) ?? `Account ${u.short_id}`),
        u.beta_access ? badge('Beta access', 'ok') : badge('No access', 'warn'),
      ),
      facts([
        ['ID', u.short_id],
        [
          'Sign-in',
          isRelay(emailOf(u)) ? h('span', {}, `${u.provider} `, badge('Hide My Email', 'plain')) : u.provider,
        ],
        ['Last sync', fmtTime(u.last_sync_at)],
        ['Created', fmtTime(u.created_at)],
      ]),
      actions,
      failure.el,
    );
  }

  listen(filter, 'input', render);
  const root = h(
    'div',
    {},
    h('h1', { class: 'adm-h1', id: 'adm-title', tabindex: -1 }, 'Users'),
    h(
      'div',
      { class: 'adm-bar' },
      field('Search', filter, 'By email (when shown), ID or sign-in.'),
      emailToggle(ctx, () => {
        // Off hides the addresses at once (render() asks the switch), then asks again.
        render();
        void load();
      }),
    ),
    count,
    list,
  );
  void load();
  return root;
}

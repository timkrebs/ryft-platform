// Invites: create a code (optionally for one email address), copy and share it, list, revoke.
import { describeError } from '../api';
import { h, listen } from '../dom';
import { fmtTime, inDays, isRelay } from '../format';
import type { BetaInvite, InviteInput, InviteStatus } from '../types';
import {
  badge,
  button,
  confirmButton,
  emailToggle,
  errorBox,
  facts,
  field,
  Handled,
  input,
  note,
  type Ctx,
  type Tone,
} from '../ui';

/** A link is shared or copied only when it is a RYFT invite link: whatever the api sent. */
const INVITE_LINK = 'https://ryftapp.de/invite/';
const linkOk = (link: string) => typeof link === 'string' && link.startsWith(INVITE_LINK);

const TONE: Record<InviteStatus, Tone> = { open: 'ok', used: 'plain', expired: 'warn', revoked: 'bad' };
const EXPIRY: [string, number][] = [
  ['14 days', 14],
  ['7 days', 7],
  ['1 day', 1],
  ['30 days', 30],
  ['Never', 0],
];

export function invitesScreen(ctx: Ctx): HTMLElement {
  const result = h('div', { class: 'adm-result' });
  const list = h('div', { class: 'adm-list-wrap', 'aria-busy': 'true' }, note('Loading…'));

  async function load() {
    list.setAttribute('aria-busy', 'true');
    try {
      const q = ctx.showEmails() ? '?emails=1' : '';
      const invites = await ctx.call(() => ctx.api.get<BetaInvite[]>(`/v1/admin/invites${q}`));
      list.replaceChildren(renderList(invites));
    } catch (e) {
      if (!(e instanceof Handled)) list.replaceChildren(errorBox(describeError(e)));
    } finally {
      list.setAttribute('aria-busy', 'false');
    }
  }

  function renderList(invites: BetaInvite[]): HTMLElement {
    if (invites.length === 0) return note('No invite codes yet.');
    const ul = h('ul', { class: 'adm-cards' });
    for (const inv of invites) ul.append(card(inv));
    return ul;
  }

  function card(inv: BetaInvite): HTMLElement {
    const whom = inv.email ?? (inv.status === 'open' ? 'Anyone with the code' : 'No address stored');
    const item = h(
      'li',
      { class: 'adm-card' },
      h(
        'div',
        { class: 'adm-card-head' },
        h('span', { class: 'adm-code' }, inv.code),
        badge(inv.status, TONE[inv.status] ?? 'plain'),
      ),
      facts([
        ['For', whom],
        ['Note', inv.note || null],
        ['Uses', `${inv.uses} of ${inv.max_uses}`],
        ['Expires', inv.expires_at ? fmtTime(inv.expires_at) : 'Never'],
        ['Created', fmtTime(inv.created_at)],
      ]),
    );
    if (inv.uses_by.length > 0 || inv.uses > 0) {
      const used = h('ul', { class: 'adm-uses', 'aria-label': `Redeemed ${inv.code}` });
      for (const u of inv.uses_by) {
        used.append(
          h(
            'li',
            {},
            u.email ?? `Account ${u.short_id}`,
            u.private_relay || isRelay(u.email) ? ' ' : null,
            u.private_relay || isRelay(u.email) ? badge('Hide My Email', 'plain') : null,
            ` · ${fmtTime(u.at)}`,
          ),
        );
      }
      if (inv.uses > inv.uses_by.length) {
        used.append(h('li', {}, `${inv.uses - inv.uses_by.length} more by an account deleted since`));
      }
      item.append(h('p', { class: 'adm-sub' }, 'Redeemed by'), used);
    }
    const actions = h('div', { class: 'adm-actions' });
    if (linkOk(inv.link)) actions.append(copyButton('Copy link', inv.link));
    actions.append(copyButton('Copy code', inv.code));
    if (inv.status === 'open') {
      actions.append(
        confirmButton('Revoke', 'Revoke now', async () => {
          try {
            await ctx.call(() => ctx.api.del(`/v1/admin/invites/${encodeURIComponent(inv.code)}`));
            ctx.announce(`Revoked ${inv.code}`);
            await load();
          } catch (e) {
            if (!(e instanceof Handled)) {
              actions.append(errorBox(describeError(e)));
            }
          }
        }),
      );
    }
    item.append(actions);
    return item;
  }

  function copyButton(label: string, text: string): HTMLElement {
    return button(label, 'secondary', async (b) => {
      const ok = await copy(text);
      ctx.announce(ok ? 'Copied' : 'Copy failed. Select the text and copy it by hand.');
      const before = b.textContent;
      if (ok) {
        b.textContent = 'Copied';
        setTimeout(() => {
          b.textContent = before;
        }, 1500);
      }
    });
  }

  function showResult(inv: BetaInvite) {
    const share =
      typeof navigator.share === 'function' && linkOk(inv.link)
        ? button('Share', 'primary', async () => {
            try {
              await navigator.share({
                title: 'RYFT beta invite',
                text: `Your RYFT invite code: ${inv.code}`,
                url: inv.link,
              });
            } catch {
              // The sheet was dismissed, or sharing is not possible here: Copy stays.
            }
          })
        : null;
    result.replaceChildren(
      h(
        'section',
        { class: 'adm-card adm-new', 'aria-labelledby': 'adm-new-code' },
        h('h2', { class: 'adm-h2', id: 'adm-new-code' }, 'New invite'),
        h('p', { class: 'adm-bigcode', 'data-testid': 'new-code' }, inv.code),
        h('p', { class: 'adm-link', 'data-testid': 'new-link' }, inv.link),
        inv.email ? h('p', { class: 'adm-sub' }, `Only for ${inv.email}`) : null,
        h(
          'div',
          { class: 'adm-actions' },
          share,
          linkOk(inv.link) ? copyButton('Copy link', inv.link) : null,
          copyButton('Copy code', inv.code),
          button('Done', 'secondary', () => result.replaceChildren()),
        ),
      ),
    );
    result.querySelector<HTMLElement>('#adm-new-code')?.setAttribute('tabindex', '-1');
    result.querySelector<HTMLElement>('#adm-new-code')?.focus();
  }

  const form = buildForm();
  function buildForm(): HTMLFormElement {
    const email = input('email', 'email', {
      autocomplete: 'off',
      autocapitalize: 'none',
      spellcheck: 'false',
      inputmode: 'email',
    });
    const noteInput = input('text', 'note', { maxlength: 200, autocomplete: 'off' });
    const uses = input('number', 'max_uses', { min: 1, max: 1000, value: 1, inputmode: 'numeric' });
    const expiry = h('select', { name: 'expiry', class: 'adm-input' });
    for (const [label, days] of EXPIRY) expiry.append(h('option', { value: days }, label));
    const err = h('div', {});
    const submit = h('button', { type: 'submit', class: 'adm-btn adm-btn-primary' }, 'Create invite');

    listen(email, 'input', () => {
      const bound = email.value.trim() !== '';
      uses.disabled = bound;
      if (bound) uses.value = '1';
    });

    const f = h(
      'form',
      { class: 'adm-form', novalidate: true, 'aria-labelledby': 'adm-form-title' },
      h('h2', { class: 'adm-h2', id: 'adm-form-title' }, 'New invite'),
      field(
        'Email (optional)',
        email,
        'Sign in with Apple can hide the email address. If the tester did, leave the email empty: anyone with the code can then join.',
      ),
      field('Note (optional)', noteInput, 'Only you see it.'),
      field('Uses', uses, 'A code for one email address has exactly one use.'),
      field('Expires after', expiry),
      err,
      submit,
    );
    listen(f, 'submit', async (ev) => {
      ev.preventDefault();
      err.replaceChildren();
      const body: InviteInput = { max_uses: email.value.trim() ? 1 : Number(uses.value) };
      if (!Number.isInteger(body.max_uses) || body.max_uses < 1 || body.max_uses > 1000) {
        err.replaceChildren(errorBox('Uses must be a whole number from 1 to 1000.'));
        return;
      }
      if (email.value.trim()) body.email = email.value.trim();
      if (noteInput.value.trim()) body.note = noteInput.value.trim();
      const days = Number(expiry.value);
      if (days > 0) body.expires_at = inDays(days, ctx.now());
      submit.disabled = true;
      try {
        const created = await ctx.call(() => ctx.api.post<BetaInvite>('/v1/admin/invites', body));
        f.reset();
        uses.disabled = false;
        showResult(created);
        ctx.announce('Invite created');
        await load();
      } catch (e) {
        if (!(e instanceof Handled)) err.replaceChildren(errorBox(describeError(e)));
      } finally {
        submit.disabled = false;
      }
    });
    return f;
  }

  const root = h(
    'div',
    {},
    h('h1', { class: 'adm-h1', id: 'adm-title', tabindex: -1 }, 'Invites'),
    form,
    result,
    h(
      'div',
      { class: 'adm-bar' },
      h('h2', { class: 'adm-h2' }, 'All codes'),
      emailToggle(ctx, () => void load()),
    ),
    list,
  );
  void load();
  return root;
}

/** Writes to the clipboard; false when the browser refuses. */
async function copy(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

export { copy };

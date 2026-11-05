// Flags, read-only: changing one stays with the scripts (the api answers a token 403 there).
// The count of allow-listed accounts is shown, never their ids.
import { describeError } from '../api';
import { h } from '../dom';
import { fmtTime } from '../format';
import type { FeatureFlag } from '../types';
import { badge, errorBox, facts, Handled, note, type Ctx } from '../ui';

export function flagsScreen(ctx: Ctx): HTMLElement {
  const list = h('div', { 'aria-busy': 'true' }, note('Loading…'));

  async function load() {
    try {
      const flags = await ctx.call(() => ctx.api.get<FeatureFlag[]>('/v1/admin/flags'));
      if (flags.length === 0) {
        list.replaceChildren(note('No flags.'));
      } else {
        const ul = h('ul', { class: 'adm-cards adm-grid' });
        for (const f of flags) {
          ul.append(
            h(
              'li',
              { class: 'adm-card' },
              h(
                'div',
                { class: 'adm-card-head' },
                h('span', { class: 'adm-code' }, f.key),
                f.enabled ? badge('On', 'ok') : badge('Off', 'plain'),
              ),
              facts([
                ['Rollout', `${f.rollout_pct}%`],
                ['Allow-listed accounts', String(f.allow_user_ids?.length ?? 0)],
                ['Updated', fmtTime(f.updated_at)],
              ]),
            ),
          );
        }
        list.replaceChildren(ul);
      }
    } catch (e) {
      if (!(e instanceof Handled)) list.replaceChildren(errorBox(describeError(e)));
    } finally {
      list.setAttribute('aria-busy', 'false');
    }
  }

  const root = h(
    'div',
    {},
    h('h1', { class: 'adm-h1', id: 'adm-title', tabindex: -1 }, 'Flags'),
    note('Read-only. Changing a flag stays with the scripts.'),
    list,
  );
  void load();
  return root;
}

// Feedback: what testers wrote (FeedbackBrief: no device fields, no request ids, no diagnostics).
// The message is the tester's own text, rendered as text only.
import { describeError } from '../api';
import { h } from '../dom';
import { daysAgo, fmtTime } from '../format';
import type { FeedbackBrief } from '../types';
import { badge, button, errorBox, facts, Handled, note, type Ctx } from '../ui';

const WINDOW_DAYS = 30;

export function feedbackScreen(ctx: Ctx): HTMLElement {
  const list = h('div', { 'aria-busy': 'true' }, note('Loading…'));

  async function load() {
    list.setAttribute('aria-busy', 'true');
    try {
      const since = encodeURIComponent(daysAgo(WINDOW_DAYS, ctx.now()));
      const items = await ctx.call(() => ctx.api.get<FeedbackBrief[]>(`/v1/admin/feedback?since=${since}`));
      if (items.length === 0) {
        list.replaceChildren(note(`No feedback in the last ${WINDOW_DAYS} days.`));
      } else {
        const ul = h('ul', { class: 'adm-cards' });
        for (const f of items) ul.append(card(f));
        list.replaceChildren(ul);
      }
    } catch (e) {
      if (!(e instanceof Handled)) list.replaceChildren(errorBox(describeError(e)));
    } finally {
      list.setAttribute('aria-busy', 'false');
    }
  }

  function card(f: FeedbackBrief): HTMLElement {
    const actions = h('div', { class: 'adm-actions' });
    if (!f.resolved_at) {
      actions.append(
        button('Resolve', 'primary', async (b) => {
          b.disabled = true;
          try {
            await ctx.call(() =>
              ctx.api.post(`/v1/admin/feedback/${encodeURIComponent(String(f.id))}/resolve`),
            );
            ctx.announce('Marked as resolved');
            await load();
          } catch (e) {
            b.disabled = false;
            if (!(e instanceof Handled)) actions.append(errorBox(describeError(e)));
          }
        }),
      );
    }
    return h(
      'li',
      { class: 'adm-card' },
      h(
        'div',
        { class: 'adm-card-head' },
        h('span', { class: 'adm-title' }, f.category),
        f.resolved_at ? badge('Resolved', 'ok') : badge('Open', 'warn'),
      ),
      h('p', { class: 'adm-message' }, f.message),
      facts([
        ['Account', f.user_short_id],
        ['Sent', fmtTime(f.created_at)],
        ['Resolved', f.resolved_at ? fmtTime(f.resolved_at) : null],
      ]),
      actions,
    );
  }

  const root = h('div', {}, h('h1', { class: 'adm-h1', id: 'adm-title', tabindex: -1 }, 'Feedback'), list);
  void load();
  return root;
}

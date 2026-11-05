// Shared pieces of the screens: the context they get, buttons, fields, badges.
import type { AdminApi } from './api';
import { h, listen, uid, type Child } from './dom';

/** Thrown by Ctx.call after the app already moved to another view (sign-in, TOTP). Screens ignore it. */
export class Handled extends Error {
  constructor() {
    super('handled');
    this.name = 'Handled';
  }
}

export interface Ctx {
  api: AdminApi;
  now: () => number;
  /** Runs an api call; a lost session or a missing second factor moves the app and throws Handled. */
  call<T>(fn: () => Promise<T>): Promise<T>;
  /** Emails are asked for (?emails=1) only while this is on. Off by default, kept in memory. */
  showEmails(): boolean;
  setShowEmails(on: boolean): void;
  /** A short message for screen readers (and a visible hint where the screen shows one). */
  announce(message: string): void;
}

export type ButtonKind = 'primary' | 'secondary' | 'danger';

export function button(
  label: string,
  kind: ButtonKind,
  onClick: (b: HTMLButtonElement) => void,
  extra: Record<string, string> = {},
) {
  const b = h('button', { type: 'button', class: `adm-btn adm-btn-${kind}`, ...extra }, label);
  return listen(b, 'click', () => onClick(b));
}

/** A label with its control; `hint` is wired with aria-describedby. */
export function field(label: string, control: HTMLElement, hint?: string): HTMLElement {
  const id = control.id || uid('f');
  control.id = id;
  const wrap = h('div', { class: 'adm-field' }, h('label', { for: id }, label), control);
  if (hint) {
    const hintId = `${id}-hint`;
    control.setAttribute('aria-describedby', hintId);
    wrap.append(h('p', { class: 'adm-hint', id: hintId }, hint));
  }
  return wrap;
}

export function input(type: string, name: string, attrs: Record<string, string | number | boolean> = {}) {
  return h('input', { type, name, class: 'adm-input', ...attrs });
}

export type Tone = 'ok' | 'warn' | 'bad' | 'plain';

export function badge(text: string, tone: Tone = 'plain'): HTMLElement {
  return h('span', { class: `adm-badge adm-badge-${tone}` }, text);
}

/** A <dl> of label / value pairs; values may be text or nodes. Rows with a null value are left out. */
export function facts(rows: [string, Child][]): HTMLElement {
  const dl = h('dl', { class: 'adm-facts' });
  for (const [k, v] of rows) {
    if (v === null || v === undefined || v === false) continue;
    dl.append(h('dt', {}, k), h('dd', {}, v));
  }
  return dl;
}

export function errorBox(message: string): HTMLElement {
  return h('p', { class: 'adm-error', role: 'alert' }, message);
}

/** A place for one error: a new one replaces the last instead of piling up below it. */
export function errorSlot(): { el: HTMLElement; show(message: string): void; clear(): void } {
  const el = h('div', { class: 'adm-error-slot' });
  return {
    el,
    show: (message) => el.replaceChildren(errorBox(message)),
    clear: () => el.replaceChildren(),
  };
}

/**
 * After a list was rendered again, puts the focus back where the operator was: the first
 * button of item `key` (a card with `data-key`), else the card itself, else the screen's heading.
 */
export function refocus(list: HTMLElement, key: string): void {
  const item = [...list.querySelectorAll<HTMLElement>('[data-key]')].find((el) => el.dataset.key === key);
  const target =
    item?.querySelector<HTMLElement>('.adm-actions button:not([disabled])') ??
    item ??
    document.getElementById('adm-title');
  target?.focus();
}

export function note(message: string): HTMLElement {
  return h('p', { class: 'adm-note' }, message);
}

/** The "show email addresses" switch the invites and users screens share. */
export function emailToggle(ctx: Ctx, onChange: () => void): HTMLElement {
  const box = h('input', { type: 'checkbox', class: 'adm-check' });
  box.checked = ctx.showEmails();
  listen(box, 'change', () => {
    ctx.setShowEmails(box.checked);
    onChange();
  });
  const id = uid('emails');
  box.id = id;
  return h('div', { class: 'adm-toggle' }, box, h('label', { for: id }, 'Show email addresses'));
}

/**
 * An inline two-step action: the first press asks, the second does it. No dialogs
 * (they do not work well on a phone and are hard to test).
 */
export function confirmButton(label: string, confirmLabel: string, run: () => Promise<void>): HTMLElement {
  const holder = h('span', { class: 'adm-confirm' });
  // `focus`: the buttons were replaced under the operator's focus, so the first one takes it.
  const reset = (focus: boolean) => {
    const first = button(label, 'danger', () => {
      let busy = false;
      const yes = button(confirmLabel, 'danger', async (b) => {
        if (busy) return;
        busy = true;
        b.disabled = true;
        try {
          await run();
        } finally {
          // Still here (the action failed, or the list kept this card): back to the first step.
          if (holder.isConnected)
            reset(holder.contains(document.activeElement) || document.activeElement === document.body);
        }
      });
      holder.replaceChildren(
        yes,
        button('Cancel', 'secondary', () => reset(true)),
      );
      yes.focus();
    });
    holder.replaceChildren(first);
    if (focus) first.focus();
  };
  reset(false);
  return holder;
}

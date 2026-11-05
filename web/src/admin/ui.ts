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
  const reset = () => {
    holder.replaceChildren(
      button(label, 'danger', () => {
        const yes = button(confirmLabel, 'danger', async (b) => {
          b.disabled = true;
          try {
            await run();
          } finally {
            if (holder.isConnected) {
              b.disabled = false;
              reset();
            }
          }
        });
        holder.replaceChildren(
          yes,
          button('Cancel', 'secondary', () => reset()),
        );
        yes.focus();
      }),
    );
  };
  reset();
  return holder;
}

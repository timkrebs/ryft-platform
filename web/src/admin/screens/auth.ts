// Sign in, enrol the authenticator app (first use), enter the code. Plain <form>s so a
// password manager can fill them; submission is handled here (the CSP says form-action 'none').
import { describeError, FormError } from '../api';
import { h, listen } from '../dom';
import { button, errorBox, field, input } from '../ui';
import { copy } from './invites';

type Submit<T extends unknown[]> = (...args: T) => Promise<void>;

function heading(text: string) {
  return h('h1', { class: 'adm-h1', id: 'adm-title', tabindex: -1 }, text);
}

/** A form whose submit button is disabled while the handler runs; a thrown error is shown in the form. */
function form(label: string, controls: HTMLElement[], submitLabel: string, run: () => Promise<void>) {
  const err = h('div', {});
  const submit = h('button', { type: 'submit', class: 'adm-btn adm-btn-primary' }, submitLabel);
  const f = h('form', { class: 'adm-form', novalidate: true, 'aria-label': label }, ...controls, err, submit);
  listen(f, 'submit', async (ev) => {
    ev.preventDefault();
    err.replaceChildren();
    submit.disabled = true;
    try {
      await run();
    } catch (e) {
      err.replaceChildren(errorBox(describeError(e)));
      submit.disabled = false;
    }
  });
  return f;
}

function codeInput() {
  return input('text', 'code', {
    autocomplete: 'one-time-code',
    inputmode: 'numeric',
    pattern: '[0-9]*',
    maxlength: 6,
    autocapitalize: 'none',
    spellcheck: 'false',
  });
}

function checkCode(value: string): string {
  const code = value.replace(/\s+/g, '');
  if (!/^\d{6}$/.test(code)) throw new Error('code');
  return code;
}

export function signInView(opts: {
  message?: string;
  onSubmit: Submit<[email: string, password: string]>;
}): HTMLElement {
  const email = input('email', 'email', {
    autocomplete: 'username',
    autocapitalize: 'none',
    spellcheck: 'false',
  });
  const password = input('password', 'password', { autocomplete: 'current-password' });
  return h(
    'div',
    { class: 'adm-narrow' },
    heading('Sign in'),
    opts.message ? h('p', { class: 'adm-note', role: 'status' }, opts.message) : null,
    form('Sign in', [field('Email', email), field('Password', password)], 'Continue', async () => {
      if (!email.value.trim() || !password.value) throw new FormError('Enter your email and password.');
      await opts.onSubmit(email.value.trim(), password.value);
    }),
  );
}

export function challengeView(opts: {
  reason?: string;
  onSubmit: Submit<[code: string]>;
  onCancel: () => void;
}) {
  const code = codeInput();
  return h(
    'div',
    { class: 'adm-narrow' },
    heading('Enter your code'),
    h(
      'p',
      { class: 'adm-note' },
      opts.reason ?? 'Open your authenticator app and enter the 6-digit code for RYFT admin.',
    ),
    form('Authenticator code', [field('6-digit code', code)], 'Verify', async () => {
      let value: string;
      try {
        value = checkCode(code.value);
      } catch {
        throw new FormError('The code has 6 digits.');
      }
      await opts.onSubmit(value);
    }),
    h('div', { class: 'adm-actions' }, button('Use another account', 'secondary', opts.onCancel)),
  );
}

export function enrolView(opts: {
  qr: string | null;
  secret: string;
  onSubmit: Submit<[code: string]>;
  onCancel: () => void;
}) {
  const code = codeInput();
  const live = h('span', { class: 'adm-note', role: 'status' });
  return h(
    'div',
    { class: 'adm-narrow' },
    heading('Set up your authenticator'),
    h(
      'p',
      { class: 'adm-note' },
      'Scan the code with an authenticator app, or type the secret in. Then enter the 6-digit code it shows.',
    ),
    opts.qr
      ? h('img', {
          class: 'adm-qr',
          src: opts.qr,
          alt: 'QR code for your authenticator app',
          width: 200,
          height: 200,
        })
      : null,
    h('p', { class: 'adm-sub' }, 'Secret'),
    h('p', { class: 'adm-secret', 'data-testid': 'totp-secret' }, opts.secret),
    h(
      'div',
      { class: 'adm-actions' },
      button('Copy secret', 'secondary', async () => {
        live.textContent = (await copy(opts.secret)) ? 'Copied' : 'Copy failed. Select the secret by hand.';
      }),
      live,
    ),
    form('Authenticator code', [field('6-digit code', code)], 'Verify and continue', async () => {
      let value: string;
      try {
        value = checkCode(code.value);
      } catch {
        throw new FormError('The code has 6 digits.');
      }
      await opts.onSubmit(value);
    }),
    h('div', { class: 'adm-actions' }, button('Use another account', 'secondary', opts.onCancel)),
  );
}

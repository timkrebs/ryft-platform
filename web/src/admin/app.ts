// The admin page's controller: which view is on screen, and how a lost session or a missing
// second factor moves between them.
//
//   sign in (password) → TOTP enrol (first use) or challenge → the sections
//   api 401  → refresh once, then sign in again       api 403 mfa_required → challenge
//   "Sign out" ends every session of the account (scope=global)
import { ApiError, createApi, describeError, SessionEnded } from './api';
import { config } from './config';
import { h } from './dom';
import { createAuthClient } from './gotrue';
import { clearOnLeave, type WindowLike } from './leave';
import { decodeClaims } from './jwt';
import { createSessions } from './sessions';
import { auditScreen } from './screens/audit';
import { challengeView, enrolView, signInView } from './screens/auth';
import { feedbackScreen } from './screens/feedback';
import { flagsScreen } from './screens/flags';
import { invitesScreen } from './screens/invites';
import { usersScreen } from './screens/users';
import type { KV } from './storage';
import { button, Handled, type Ctx } from './ui';

export interface Elements {
  session: HTMLElement;
  nav: HTMLElement;
  main: HTMLElement;
  live: HTMLElement;
}

const TABS = [
  ['invites', 'Invites', invitesScreen],
  ['users', 'Users', usersScreen],
  ['feedback', 'Feedback', feedbackScreen],
  ['flags', 'Flags', flagsScreen],
  ['audit', 'Audit', auditScreen],
] as const;

type TabId = (typeof TABS)[number][0];

/** sessionStorage, or a plain object when the browser blocks it (the session then ends with the page). */
function sessionKV(): KV {
  try {
    return window.sessionStorage;
  } catch {
    const m = new Map<string, string>();
    return {
      getItem: (k) => m.get(k) ?? null,
      setItem: (k, v) => void m.set(k, v),
      removeItem: (k) => void m.delete(k),
    };
  }
}

export function start(els: Elements): void {
  const auth = createAuthClient({ url: config.supabaseUrl, key: config.supabaseKey });
  const sessions = createSessions(auth, sessionKV());
  // True between a challenge started by `mfa_required` and the next successful api answer: a second
  // `mfa_required` in that window means the second factor did not take, so sign in again.
  let mfaPending = false;
  let challenging = false;
  let inApp = false;
  let emails = false;
  const api = createApi({
    baseUrl: config.apiUrl,
    sessions,
    onOk: () => {
      mfaPending = false;
    },
  });

  const ctx: Ctx = {
    api,
    now: Date.now,
    showEmails: () => emails,
    setShowEmails: (on) => {
      emails = on;
    },
    announce: (m) => {
      els.live.textContent = '';
      // A changed text is what screen readers announce.
      window.setTimeout(() => {
        els.live.textContent = m;
      }, 30);
    },
    async call<T>(fn: () => Promise<T>): Promise<T> {
      try {
        return await fn();
      } catch (e) {
        if (e instanceof SessionEnded) {
          toSignIn('Your session ended. Sign in again.');
          throw new Handled();
        }
        if (e instanceof ApiError && e.status === 403 && e.code === 'mfa_required') {
          const s = sessions.current();
          if (challenging) throw new Handled();
          if (mfaPending || !s) {
            await signOut('Sign in again to confirm with your code.');
            throw new Handled();
          }
          mfaPending = true;
          beginMfa(s.access_token, 'Confirm with your code to continue.').catch((err) =>
            toSignIn(describeError(err)),
          );
          throw new Handled();
        }
        throw e;
      }
    },
  };

  function chrome(signedIn: boolean, nav: boolean) {
    els.session.replaceChildren();
    if (signedIn)
      els.session.append(
        button('Sign out', 'secondary', () => void signOut(), { 'data-testid': 'sign-out' }),
      );
    els.nav.hidden = !nav;
    if (!nav) els.nav.replaceChildren();
  }

  function view(el: HTMLElement, focus = true) {
    els.main.replaceChildren(el);
    if (focus) els.main.querySelector<HTMLElement>('#adm-title')?.focus();
  }

  function toSignIn(message?: string) {
    inApp = false;
    challenging = false;
    chrome(false, false);
    view(
      signInView({
        message,
        onSubmit: async (email, password) => {
          const s = await auth.signInWithPassword(email, password);
          sessions.set(s);
          await beginMfa(s.access_token);
        },
      }),
      !!message,
    );
  }

  /** Enrol when the account has no verified authenticator yet, otherwise ask for a code. */
  async function beginMfa(token: string, reason?: string) {
    inApp = false;
    const user = await auth.getUser(token);
    const totp = user.factors.filter((f) => f.factor_type === 'totp');
    const verified = totp.find((f) => f.status === 'verified');
    chrome(true, false);
    if (verified) {
      challenging = true;
      view(
        challengeView({
          reason,
          onSubmit: (code) => verifyCode(verified.id, code),
          onCancel: () => void signOut(),
        }),
      );
      return;
    }
    // Leftovers from an enrolment that was never finished would block a new one.
    for (const f of totp) await auth.unenroll(token, f.id).catch(() => undefined);
    const enrol = await auth.enrolTotp(token, `ryft-admin ${new Date().toISOString().slice(0, 10)}`);
    view(
      enrolView({
        qr: enrol.qr,
        secret: enrol.secret,
        uri: enrol.uri,
        onSubmit: (code) => verifyCode(enrol.id, code),
        onCancel: () => void signOut(),
      }),
    );
  }

  async function verifyCode(factorId: string, code: string) {
    const s = await sessions.fresh();
    if (!s) {
      toSignIn('Your session ended. Sign in again.');
      return;
    }
    const challengeId = await auth.challenge(s.access_token, factorId);
    sessions.set(await auth.verify(s.access_token, factorId, challengeId, code));
    enterApp();
  }

  function enterApp() {
    challenging = false;
    const s = sessions.current();
    if (!s) return toSignIn();
    const claims = decodeClaims(s.access_token);
    if (claims?.aal !== 'aal2') {
      beginMfa(s.access_token).catch((e) => toSignIn(describeError(e)));
      return;
    }
    // A hint only; the api decides. A non-operator account ends here rather than meeting 401s.
    if (claims.appRole !== 'admin') {
      void signOut('This account is not an operator account.');
      return;
    }
    inApp = true;
    chrome(true, true);
    showTab();
  }

  function currentTab(): TabId {
    const id = window.location.hash.replace(/^#/, '');
    return TABS.find(([t]) => t === id)?.[0] ?? 'invites';
  }

  function showTab() {
    const current = currentTab();
    els.nav.replaceChildren(
      ...TABS.map(([id, label]) =>
        h('a', { href: `#${id}`, class: 'adm-tab', 'aria-current': id === current ? 'page' : null }, label),
      ),
    );
    const make = TABS.find(([id]) => id === current)![2];
    view(make(ctx));
  }

  async function signOut(message = 'You are signed out.') {
    let token: string | null = null;
    try {
      // A fresh token, so the global sign-out is not refused for an expired one.
      token = (await sessions.fresh())?.access_token ?? null;
    } catch {
      token = sessions.current()?.access_token ?? null;
    }
    sessions.clear();
    mfaPending = false;
    if (token) await auth.signOut(token).catch(() => undefined);
    toSignIn(message);
  }

  // Other pages of this origin can read sessionStorage: end the session when the tab leaves /admin.
  clearOnLeave(window as unknown as WindowLike, () => sessions.clear());

  window.addEventListener('hashchange', () => {
    if (inApp) showTab();
  });

  sessions
    .fresh()
    .then((s) => (s ? enterApp() : toSignIn()))
    .catch((e) => toSignIn(describeError(e)));
}

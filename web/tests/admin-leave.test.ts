import { describe, expect, it, vi } from 'vitest';
import { isAllowedHref } from '../src/admin/dom';
import { clearOnLeave, isSamePage, type NavigateEventLike, type WindowLike } from '../src/admin/leave';

// sessionStorage is readable by every page of the origin in this tab (and a duplicated tab),
// so the session is cleared when the tab leaves /admin.
function fake(current = 'https://ryftlab.de/admin', withNavigation = true) {
  let onNavigate: ((e: NavigateEventLike) => void) | undefined;
  let onPageHide: (() => void) | undefined;
  const win: WindowLike = {
    navigation: withNavigation
      ? {
          currentEntry: { url: current },
          addEventListener: (_t, fn) => {
            onNavigate = fn;
          },
        }
      : undefined,
    addEventListener: (_t, fn) => {
      onPageHide = fn;
    },
  };
  const clear = vi.fn();
  clearOnLeave(win, clear);
  return {
    clear,
    navigate: (url: string, sameDocument = false) => onNavigate!({ destination: { url, sameDocument } }),
    pagehide: () => onPageHide!(),
  };
}

describe('clearOnLeave', () => {
  it('clears when the next document is another page of the origin', () => {
    const w = fake();
    w.navigate('https://ryftlab.de/privacy');
    w.pagehide();
    expect(w.clear).toHaveBeenCalledOnce();
  });

  it('clears when the next document is on another origin', () => {
    const w = fake();
    w.navigate('https://example.com/admin');
    w.pagehide();
    expect(w.clear).toHaveBeenCalledOnce();
  });

  it('clears on a pagehide the Navigation API did not announce (address bar, tab close)', () => {
    const w = fake();
    w.pagehide();
    expect(w.clear).toHaveBeenCalledOnce();
  });

  it('keeps the session for a reload of /admin (same page, with or without a fragment)', () => {
    const w = fake();
    w.navigate('https://ryftlab.de/admin');
    w.pagehide();
    expect(w.clear).not.toHaveBeenCalled();
    const v = fake();
    v.navigate('https://ryftlab.de/admin#users');
    v.pagehide();
    expect(v.clear).not.toHaveBeenCalled();
  });

  it('ignores a same-document navigation (a #view change): it never unloads the page', () => {
    const w = fake();
    w.navigate('https://ryftlab.de/privacy', true);
    w.pagehide();
    expect(w.clear).toHaveBeenCalledOnce();
  });

  it('without the Navigation API every pagehide clears (a reload signs in again: the safe direction)', () => {
    const w = fake('https://ryftlab.de/admin', false);
    w.pagehide();
    expect(w.clear).toHaveBeenCalledOnce();
  });
});

describe('isSamePage', () => {
  it('compares origin and path, not the fragment or query', () => {
    expect(isSamePage('https://ryftlab.de/admin#invites', 'https://ryftlab.de/admin')).toBe(true);
    expect(isSamePage('https://ryftlab.de/admin/', 'https://ryftlab.de/admin')).toBe(false);
    expect(isSamePage('http://ryftlab.de/admin', 'https://ryftlab.de/admin')).toBe(false);
    expect(isSamePage('https://ryftlab.de/admin', null)).toBe(false);
    expect(isSamePage('not a url', 'https://ryftlab.de/admin')).toBe(false);
  });
});

describe('the links the page may build', () => {
  it('allows in-page links and the authenticator otpauth://totp/ URI, nothing else', () => {
    for (const ok of [
      '#invites',
      '#users',
      'otpauth://totp/RYFT%20admin:a@b.test?secret=ABC&issuer=RYFT%20admin',
    ]) {
      expect(isAllowedHref(ok), ok).toBe(true);
    }
    for (const bad of [
      'javascript:alert(1)',
      'data:text/html,x',
      'https://example.com',
      '//example.com',
      '/admin',
      'otpauth://hotp/x?secret=ABC',
      'otpauth://totp/x y',
      'otpauth://totp/x"onclick=1',
      '#a b',
      '',
      null,
      42,
    ]) {
      expect(isAllowedHref(bad), String(bad)).toBe(false);
    }
  });
});

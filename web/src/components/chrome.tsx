// The frame around every page: skip link, header, footer, language and theme switches.
import type { ReactNode } from 'react';
import { Logo, Lockup } from './Brand';
import { Container } from './ui';
import { format, localePath, messages, otherLocale, type Locale } from '@/lib/i18n';
import { BUILD_YEAR } from '@/lib/site';
import styles from './chrome.module.css';

type ShellProps = {
  locale: Locale;
  /** This page's path, written as the English path (`/privacy`), for the language switch. */
  path: string;
  /** Pages without a translation link to the other language's home instead. */
  translated?: boolean;
  children: ReactNode;
};

export function PageShell({ locale, path, translated = true, children }: ShellProps) {
  const t = messages(locale);
  return (
    <>
      <a className="skip-link" href="#main">
        {t.a11y.skip}
      </a>
      <Header locale={locale} path={path} translated={translated} />
      <main id="main" tabIndex={-1} className={styles.main}>
        {children}
      </main>
      <Footer locale={locale} path={path} translated={translated} />
    </>
  );
}

function Header({ locale, path, translated }: Omit<ShellProps, 'children'>) {
  const t = messages(locale);
  const home = localePath(locale, '/');
  return (
    <header className={styles.header}>
      <Container className={styles.headerRow}>
        <a href={home} className={styles.home} aria-label={t.a11y.home}>
          <Logo />
        </a>
        <nav aria-label={t.a11y.mainNav} className={styles.nav}>
          <ul>
            <li>
              <a href={home === '/' ? '/#how' : `${home}#how`}>{t.nav.how}</a>
            </li>
            <li>
              <a
                href={localePath(locale, '/science')}
                aria-current={path === '/science' ? 'page' : undefined}
              >
                {t.nav.science}
              </a>
            </li>
            <li>
              <a href={localePath(locale, '/beta')} aria-current={path === '/beta' ? 'page' : undefined}>
                {t.nav.beta}
              </a>
            </li>
          </ul>
        </nav>
        <LanguageSwitch locale={locale} path={path} translated={translated} className={styles.lang} />
      </Container>
    </header>
  );
}

function Footer({ locale, path, translated }: Omit<ShellProps, 'children'>) {
  const t = messages(locale);
  const link = (p: string) => localePath(locale, p);
  const items: [string, string][] = [
    [link('/privacy'), t.footer.privacy],
    [link('/terms'), t.footer.terms],
    [link('/support'), t.footer.support],
    [link('/licenses'), t.footer.licenses],
    [link('/impressum'), t.footer.impressum],
  ];
  return (
    <footer className={styles.footer}>
      <Container className={styles.footerGrid}>
        <div className={styles.footerBrand}>
          <Lockup tagline={t.site.tagline} />
          <p className={styles.wellness}>{t.footer.wellness}</p>
        </div>
        <nav aria-label={t.a11y.footerNav} className={styles.footerNav}>
          <ul>
            {items.map(([href, label]) => (
              <li key={href}>
                <a href={href} aria-current={href === link(path) ? 'page' : undefined}>
                  {label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
        <div className={styles.footerControls}>
          <LanguageSwitch locale={locale} path={path} translated={translated} withName />
          <ThemeSwitch locale={locale} />
        </div>
        <div className={styles.footerMeta}>
          <p>{format(t.footer.copyright, { year: BUILD_YEAR })}</p>
          <p>{t.footer.noTracking}</p>
        </div>
      </Container>
    </footer>
  );
}

/** A plain link to this page in the other language (no script, no cookie). */
export function LanguageSwitch({
  locale,
  path,
  translated = true,
  withName = false,
  className,
}: {
  locale: Locale;
  path: string;
  translated?: boolean;
  withName?: boolean;
  className?: string;
}) {
  const t = messages(locale);
  const other = otherLocale(locale);
  const href = localePath(other, translated ? path : '/');
  return (
    <p className={`${styles.language} ${className ?? ''}`}>
      {withName && (
        <span className={styles.languageCurrent}>
          <span className="visually-hidden">{t.a11y.language}: </span>
          {t.language.name}
        </span>
      )}
      <a href={href} hrefLang={other} lang={other} title={t.language.switchHint}>
        {t.language.switchLabel}
      </a>
    </p>
  );
}

/**
 * System / Light / Dark, like the app's Appearance setting. Hidden until the inline
 * script (src/lib/inline-scripts.ts) shows it: without JavaScript the site follows the
 * system setting and offers no control that can't work.
 */
export function ThemeSwitch({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const options = [
    ['system', t.theme.system],
    ['light', t.theme.light],
    ['dark', t.theme.dark],
  ] as const;
  return (
    <div className={styles.theme} role="group" aria-label={t.a11y.theme} data-theme-switch hidden>
      {options.map(([value, label]) => (
        <button key={value} type="button" data-theme-option={value} aria-pressed={value === 'system'}>
          {label}
        </button>
      ))}
    </div>
  );
}

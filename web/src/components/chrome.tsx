// The frame around every page: skip link, header, footer.
import type { ReactNode } from 'react';
import { Logo, Lockup } from './Brand';
import { Container } from './ui';
import { format, localePath, messages, type Locale } from '@/lib/i18n';
import { BUILD_YEAR } from '@/lib/site';
import styles from './chrome.module.css';

type ShellProps = {
  locale: Locale;
  /** This page's path (`/privacy`), for `aria-current` in the navigation. */
  path: string;
  children: ReactNode;
};

export function PageShell({ locale, path, children }: ShellProps) {
  const t = messages(locale);
  return (
    <>
      <a className="skip-link" href="#main">
        {t.a11y.skip}
      </a>
      <Header locale={locale} path={path} />
      <main id="main" tabIndex={-1} className={styles.main}>
        {children}
      </main>
      <Footer locale={locale} path={path} />
    </>
  );
}

function Header({ locale, path }: Omit<ShellProps, 'children'>) {
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
      </Container>
    </header>
  );
}

function Footer({ locale, path }: Omit<ShellProps, 'children'>) {
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
        <div className={styles.footerMeta}>
          <p>{format(t.footer.copyright, { year: BUILD_YEAR })}</p>
          <p>{t.footer.noTracking}</p>
        </div>
      </Container>
    </footer>
  );
}

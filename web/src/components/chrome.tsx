// The frame around every page: skip link, header and footer. No theme switch: the site is
// dark-only, like the app (web D20). The header is the app's: the lock-up leading, the dot tab bar
// as the navigation; the footer is the app's grouped list (web/docs/NOCTURNE.md).
import type { ReactNode } from 'react';
import { Logo, Lockup } from './Brand';
import { Container, Group, GroupLink } from './ui';
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
        {/* The app's tab bar as a nav: a dot above each label, the current page's dot is Volt. */}
        <nav aria-label={t.a11y.mainNav} className={styles.nav}>
          <ul>
            <li>
              <a href={home === '/' ? '/#how' : `${home}#how`}>
                <span className={styles.dot} aria-hidden="true" />
                {t.nav.how}
              </a>
            </li>
            <li>
              <a
                href={localePath(locale, '/science')}
                aria-current={path === '/science' ? 'page' : undefined}
              >
                <span className={styles.dot} aria-hidden="true" />
                {t.nav.science}
              </a>
            </li>
            <li>
              <a href={localePath(locale, '/beta')} aria-current={path === '/beta' ? 'page' : undefined}>
                <span className={styles.dot} aria-hidden="true" />
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
        {/* Profile's grouped list: a surface card of rows, a hairline between them, a chevron each. */}
        <nav aria-label={t.a11y.footerNav} className={styles.footerNav}>
          <Group>
            {items.map(([href, label]) => (
              <GroupLink key={href} href={href} current={href === link(path)}>
                {label}
              </GroupLink>
            ))}
          </Group>
        </nav>
        <div className={styles.footerMeta}>
          <p>{format(t.footer.copyright, { year: BUILD_YEAR })}</p>
          <p>{t.footer.noTracking}</p>
        </div>
      </Container>
    </footer>
  );
}

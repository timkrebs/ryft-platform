// The frame around every page: skip link, the hairline header with its nav and call to
// action (a <details> menu on narrow screens: no script), the main landmark, the footer with
// the wordmark set large.
import type { ReactNode } from 'react';
import { Logo, Wordmark } from './Brand';
import { ButtonLink, Container } from './ui';
import { format, localePath, messages, type Locale } from '@/lib/i18n';
import { BUILD_YEAR } from '@/lib/site';
import styles from './chrome.module.css';

type ShellProps = {
  locale: Locale;
  /** This page's path (`/privacy`), for `aria-current`. */
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

function navItems(locale: Locale, path: string) {
  const t = messages(locale).nav;
  const home = localePath(locale, '/');
  const anchor = (id: string) => (path === '/' ? `#${id}` : `${home}#${id}`);
  return [
    [anchor('scores'), t.scores, false],
    [anchor('systems'), t.systems, false],
    [localePath(locale, '/science'), t.science, path === '/science'],
    [anchor('pricing'), t.pricing, false],
    [anchor('faq'), t.faq, false],
    [localePath(locale, '/beta'), t.beta, path === '/beta'],
  ] as const;
}

function Header({ locale, path }: Omit<ShellProps, 'children'>) {
  const t = messages(locale);
  const home = localePath(locale, '/');
  const items = navItems(locale, path);
  return (
    <header className={styles.header}>
      <Container className={styles.headerRow}>
        <a href={home} className={styles.home} aria-label={t.a11y.home}>
          <Logo />
        </a>
        <nav aria-label={t.a11y.mainNav} className={styles.nav}>
          <ul>
            {items.map(([href, label, current]) => (
              <li key={href}>
                <a href={href} aria-current={current ? 'page' : undefined}>
                  {label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
        <ButtonLink href={localePath(locale, '/beta')} arrow className={styles.cta}>
          {t.nav.cta}
        </ButtonLink>
        <details className={styles.menu}>
          <summary>
            <span>{t.nav.menu}</span>
          </summary>
          <nav aria-label={t.a11y.mainNav}>
            <ul>
              {items.map(([href, label, current]) => (
                <li key={href}>
                  <a href={href} aria-current={current ? 'page' : undefined}>
                    {label}
                  </a>
                </li>
              ))}
              <li>
                <a href={localePath(locale, '/beta')} className={styles.menuCta}>
                  {t.nav.cta} ↗
                </a>
              </li>
            </ul>
          </nav>
        </details>
      </Container>
    </header>
  );
}

function Footer({ locale, path }: Omit<ShellProps, 'children'>) {
  const t = messages(locale);
  const link = (p: string) => localePath(locale, p);
  const columns: [string, [string, string][]][] = [
    [
      t.footer.product,
      [
        [link('/science'), t.footer.science],
        [link('/beta'), t.footer.beta],
        [link('/support'), t.footer.support],
      ],
    ],
    [
      t.footer.legal,
      [
        [link('/privacy'), t.footer.privacy],
        [link('/terms'), t.footer.terms],
        [link('/licenses'), t.footer.licenses],
        [link('/impressum'), t.footer.impressum],
      ],
    ],
  ];
  return (
    <footer className={styles.footer}>
      <Container>
        <div className={styles.footerTop}>
          <p className={styles.footerBrand}>
            <Wordmark className={styles.footerWordmark} />
            <span className={styles.footerTagline}>{t.site.tagline}</span>
          </p>
          <div className={styles.footerColumns}>
            {columns.map(([heading, items]) => (
              <nav key={heading} aria-label={heading} className={styles.footerNav}>
                <p className={styles.footerHeading}>{heading}</p>
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
            ))}
          </div>
        </div>
        <div className={styles.footerMeta}>
          <p>{format(t.footer.copyright, { year: BUILD_YEAR })}</p>
          <p>{t.footer.wellness}</p>
          <p>{t.footer.noTracking}</p>
        </div>
      </Container>
    </footer>
  );
}

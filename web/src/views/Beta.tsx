import { PageShell } from '@/components/chrome';
import { ButtonLink, Container, Label, Section, Todo } from '@/components/ui';
import { localePath, messages, type Locale } from '@/lib/i18n';
import { pageMetadata } from '@/lib/metadata';
import { SUPPORT_EMAIL, TESTFLIGHT_URL } from '@/lib/site';
import styles from './Beta.module.css';

export function betaMetadata(locale: Locale) {
  const t = messages(locale).beta;
  return pageMetadata({ locale, path: '/beta', title: t.title, description: t.description });
}

/** A mailto: link to the support address; no form and no data collection on the site. */
export function inviteHref(locale: Locale): string | null {
  if (!SUPPORT_EMAIL) return null;
  const t = messages(locale).beta;
  const query = `subject=${encodeURIComponent(t.inviteSubject)}&body=${encodeURIComponent(t.inviteBody)}`;
  return `mailto:${SUPPORT_EMAIL}?${query}`;
}

export function BetaPage({ locale }: { locale: Locale }) {
  const t = messages(locale).beta;
  const invite = inviteHref(locale);
  return (
    <PageShell locale={locale} path="/beta">
      <Container>
        <header className={styles.header}>
          <Label tone="accent">{t.label}</Label>
          <h1>{t.heading}</h1>
          <p className={styles.lead}>{t.lead}</p>
          <div className={styles.actions}>
            {TESTFLIGHT_URL && <ButtonLink href={TESTFLIGHT_URL}>{t.testflight}</ButtonLink>}
            {invite ? (
              <ButtonLink href={invite} variant={TESTFLIGHT_URL ? 'secondary' : 'primary'}>
                {t.invite}
              </ButtonLink>
            ) : (
              <p>
                {t.invite}: <Todo>{t.missingEmail}</Todo>
              </p>
            )}
          </div>
          {invite && <p className={styles.note}>{t.noForm}</p>}
        </header>

        <Section id="who" title={t.who.title}>
          <ul className={styles.checks}>
            {t.who.items.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </Section>

        <Section id="join" title={t.steps.title}>
          <ol className={styles.steps}>
            {t.steps.items.map((step, i) => (
              <li key={step.title}>
                <span className={styles.number} aria-hidden="true">
                  {String(i + 1).padStart(2, '0')}
                </span>
                <h3>{step.title}</h3>
                <p>{step.body}</p>
              </li>
            ))}
          </ol>
        </Section>

        <Section id="testers" title={t.testers.title}>
          <ul className={styles.checks}>
            {t.testers.items.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
          <p className={styles.privacy}>
            {t.privacy} <a href={localePath(locale, '/privacy')}>{t.privacyLink}</a>
          </p>
        </Section>
      </Container>
    </PageShell>
  );
}

import { PageShell } from '@/components/chrome';
import { Prose, mdxComponents } from '@/components/prose';
import { Screenshot } from '@/components/Screenshot';
import { ButtonLink, Container, Label, Todo } from '@/components/ui';
import { format, intlLocale, localePath, messages, type Locale } from '@/lib/i18n';
import { isVersioned, loadLegal, versionsOf, type LegalDoc } from '@/lib/legal';
import { pageMetadata } from '@/lib/metadata';
import { SUPPORT_EMAIL } from '@/lib/site';
import styles from './Legal.module.css';

const docPath = (doc: LegalDoc, version?: string) => (version ? `/${doc}/${version}` : `/${doc}`);

export async function legalMetadata(locale: Locale, doc: LegalDoc, version?: string) {
  const { data } = await loadLegal(locale, doc, version);
  const latest = isVersioned(doc) ? (await versionsOf(locale, doc))[0] : undefined;
  const meta = pageMetadata({
    locale,
    path: docPath(doc, version),
    title: data.title,
    // Drafts stay out of search engines until the user publishes them.
    noindex: data.status !== 'published',
  });
  // The newest version's permalink points at /<doc> as the canonical page.
  if (version && version === latest) {
    meta.alternates = {
      ...meta.alternates,
      canonical: pageMetadata({ locale, path: docPath(doc) }).alternates?.canonical,
    };
  }
  return meta;
}

export async function LegalPage({
  locale,
  doc,
  version,
}: {
  locale: Locale;
  doc: LegalDoc;
  version?: string;
}) {
  const t = messages(locale);
  const { data, Content } = await loadLegal(locale, doc, version);
  const versions = isVersioned(doc) ? await versionsOf(locale, doc) : [];
  const current = versions[0];
  const shown = version ?? current;
  const date = (iso: string) =>
    new Intl.DateTimeFormat(intlLocale(locale), { dateStyle: 'long', timeZone: 'UTC' }).format(
      new Date(`${iso}T00:00:00Z`),
    );
  const draft = data.status !== 'published';
  const older = versions.filter((v) => v !== shown);
  return (
    <PageShell locale={locale} path={docPath(doc, version)}>
      <Container narrow className={styles.page}>
        {draft && (
          <aside className={styles.draft} aria-labelledby="draft-title">
            <p id="draft-title" className={styles.draftTitle}>
              {t.legal.draftTitle}
            </p>
            {data.draftNote && <p>{data.draftNote}</p>}
          </aside>
        )}
        <header className={styles.header}>
          <h1>{data.title}</h1>
          {isVersioned(doc) && (
            <p className={styles.meta}>
              <span>
                {format(t.legal.version, { version: data.version })}
                {data.versionNote && ` (${data.versionNote})`}
              </span>
              <span>
                {data.effective
                  ? format(t.legal.inForce, { date: date(data.effective) })
                  : t.legal.notInForce}
              </span>
              <span>
                {t.legal.previous}:{' '}
                {older.length === 0
                  ? t.legal.none
                  : older.map((v, i) => (
                      <span key={v}>
                        {i > 0 && ', '}
                        <a href={localePath(locale, docPath(doc, v))}>{v}</a>
                      </span>
                    ))}
              </span>
            </p>
          )}
          {version && version !== current && (
            <p className={styles.archived}>
              {t.legal.archived} <a href={localePath(locale, docPath(doc))}>{t.legal.toCurrent}</a>
            </p>
          )}
        </header>
        <Prose>
          <Content components={mdxComponents} />
        </Prose>
        {doc === 'support' && <SupportExtras locale={locale} />}
      </Container>
    </PageShell>
  );
}

/** Support: the export and deletion steps with the app's Account card, and contact. */
function SupportExtras({ locale }: { locale: Locale }) {
  const t = messages(locale).legal;
  return (
    <>
      <section className={styles.extra} aria-labelledby="steps-title">
        <h2 id="steps-title">{t.steps.title}</h2>
        <div className={styles.stepsGrid}>
          {[t.steps.export, t.steps.delete].map((block) => (
            <div key={block.title} className={styles.stepBlock}>
              <Label as="h3">{block.title}</Label>
              <ol>
                {block.items.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ol>
            </div>
          ))}
        </div>
        <figure className={styles.shot}>
          <Screenshot id="profile-account" alt={t.steps.alt} sizes="(min-width: 46rem) 640px, 92vw" />
        </figure>
      </section>
      <section className={styles.extra} aria-labelledby="contact-title">
        <h2 id="contact-title">{t.contact.title}</h2>
        {SUPPORT_EMAIL && (
          <p>
            <ButtonLink href={`mailto:${SUPPORT_EMAIL}`}>{t.contact.button}</ButtonLink>
          </p>
        )}
        <p>
          {t.contact.response} <Todo>{t.contact.responseTodo}</Todo>
        </p>
      </section>
    </>
  );
}

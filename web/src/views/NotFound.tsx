import { PageShell } from '@/components/chrome';
import { ButtonLink, Container } from '@/components/ui';
import { localePath, messages } from '@/lib/i18n';
import styles from './NotFound.module.css';

/** One page for both languages: English first, German below (the URL decides nothing). */
export function NotFoundPage() {
  const en = messages('en').notFound;
  const de = messages('de').notFound;
  return (
    <PageShell locale="en" path="/" translated={false}>
      <Container narrow className={styles.wrap}>
        <p className={styles.code} aria-hidden="true">
          404
        </p>
        <h1>{en.title}</h1>
        <p className={styles.body}>{en.body}</p>
        <div className={styles.actions}>
          <ButtonLink href="/">{en.home}</ButtonLink>
          <ButtonLink href="/support" variant="secondary">
            {en.support}
          </ButtonLink>
        </div>
        <section lang="de" className={styles.other} aria-labelledby="not-found-de">
          <h2 id="not-found-de">{de.title}</h2>
          <p className={styles.body}>{de.body}</p>
          <p>
            <a href={localePath('de', '/')}>{de.home}</a>
          </p>
        </section>
      </Container>
    </PageShell>
  );
}

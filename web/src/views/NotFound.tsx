import { PageShell } from '@/components/chrome';
import { ButtonLink, Container } from '@/components/ui';
import { messages } from '@/lib/i18n';
import styles from './NotFound.module.css';

/** One page for every unknown URL (the URL decides nothing). */
export function NotFoundPage() {
  const t = messages('en').notFound;
  return (
    <PageShell locale="en" path="/">
      <Container narrow className={styles.wrap}>
        <p className={styles.code} aria-hidden="true">
          404
        </p>
        <h1>{t.title}</h1>
        <p className={styles.body}>{t.body}</p>
        <div className={styles.actions}>
          <ButtonLink href="/">{t.home}</ButtonLink>
          <ButtonLink href="/support" variant="secondary">
            {t.support}
          </ButtonLink>
        </div>
      </Container>
    </PageShell>
  );
}

import { Logo } from '@/components/Brand';
import { ButtonLink } from '@/components/ui';
import { messages } from '@/lib/i18n';
import { TESTFLIGHT_URL } from '@/lib/site';
import styles from './Open.module.css';

/**
 * What a universal link (/day/…, /sleep/…, /insights/…, /train/…, /auth/…) shows on a
 * device without the app: nginx serves this one file for all of them. No header links into
 * the site's flow, and nothing on the page reads the URL: an auth callback's query and
 * fragment carry tokens.
 */
export function OpenPage() {
  const t = messages('en').open;
  return (
    <main id="main" className={styles.main}>
      <Logo size={32} />
      <section className={styles.block} aria-labelledby="open-en">
        <h1 id="open-en">{t.title}</h1>
        <p>{t.body}</p>
        <p>{t.beta}</p>
      </section>
      <div className={styles.actions}>
        {TESTFLIGHT_URL && <ButtonLink href={TESTFLIGHT_URL}>{t.testflight}</ButtonLink>}
        <ButtonLink href="/" variant={TESTFLIGHT_URL ? 'secondary' : 'primary'}>
          {t.about}
        </ButtonLink>
      </div>
    </main>
  );
}

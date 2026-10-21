import { Logo } from '@/components/Brand';
import { ButtonLink } from '@/components/ui';
import { localePath, messages } from '@/lib/i18n';
import { TESTFLIGHT_URL } from '@/lib/site';
import styles from './Open.module.css';

/**
 * What a universal link (/day/…, /sleep/…, /insights/…, /train/…, /auth/…) shows on a
 * device without the app: nginx serves this one file for all of them. Both languages,
 * no header links into the site's flow, and nothing on the page reads the URL: an auth
 * callback's query and fragment carry tokens.
 */
export function OpenPage() {
  const en = messages('en').open;
  const de = messages('de').open;
  return (
    <main id="main" className={styles.main}>
      <Logo size={32} />
      <section className={styles.block} aria-labelledby="open-en">
        <h1 id="open-en">{en.title}</h1>
        <p>{en.body}</p>
        <p>{en.beta}</p>
      </section>
      <section className={styles.block} lang="de" aria-labelledby="open-de">
        <h2 id="open-de">{de.title}</h2>
        <p>{de.body}</p>
        <p>{de.beta}</p>
      </section>
      <div className={styles.actions}>
        {TESTFLIGHT_URL && <ButtonLink href={TESTFLIGHT_URL}>{en.testflight}</ButtonLink>}
        <ButtonLink href="/" variant={TESTFLIGHT_URL ? 'secondary' : 'primary'}>
          {en.about}
        </ButtonLink>
        <ButtonLink href={localePath('de', '/')} variant="quiet" lang="de" hrefLang="de">
          {de.about}
        </ButtonLink>
      </div>
    </main>
  );
}

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { PageShell } from '@/components/chrome';
import { Container, Label, Todo } from '@/components/ui';
import { format, messages, type Locale } from '@/lib/i18n';
import { pageMetadata } from '@/lib/metadata';
import { BUILD_YEAR } from '@/lib/site';
import styles from './Licenses.module.css';

type Component = { name: string; version: string; license: string; url: string; text: string | null };
type Licenses = { app: Component[]; web: Component[] };

// Generated at build time by scripts/licenses.mjs (prebuild).
function load(): Licenses {
  return JSON.parse(readFileSync(path.join(process.cwd(), 'src/generated/licenses.json'), 'utf8'));
}

export function licensesMetadata(locale: Locale) {
  const t = messages(locale).licenses;
  return pageMetadata({ locale, path: '/licenses', title: t.title, description: t.description });
}

export function LicensesPage({ locale }: { locale: Locale }) {
  const t = messages(locale).licenses;
  const data = load();
  const [before, after] = t.copyright.split('{holder}');
  return (
    <PageShell locale={locale} path="/licenses">
      <Container narrow className={styles.page}>
        <header className={styles.header}>
          <Label tone="accent">{t.label}</Label>
          <h1>{t.title}</h1>
          <p className={styles.copyright}>
            {format(before, { year: BUILD_YEAR })}
            <Todo>{t.holderTodo}</Todo>
            {after}
          </p>
          <p className={styles.intro}>{t.intro}</p>
        </header>
        <Group id="app" title={t.app} intro={t.appIntro} items={data.app} locale={locale} />
        <Group id="web" title={t.web} intro={t.webIntro} items={data.web} locale={locale} />
      </Container>
    </PageShell>
  );
}

function Group({
  id,
  title,
  intro,
  items,
  locale,
}: {
  id: string;
  title: string;
  intro: string;
  items: Component[];
  locale: Locale;
}) {
  const t = messages(locale).licenses;
  return (
    <section aria-labelledby={`${id}-title`} className={styles.group}>
      <h2 id={`${id}-title`}>{title}</h2>
      <p className={styles.intro}>
        {intro} {format(t.count, { n: items.length })}.
      </p>
      <ul className={styles.list}>
        {items.map((c) => (
          <li key={`${c.name}@${c.version}`}>
            <details className={styles.item}>
              <summary>
                <span className={styles.name}>{c.name}</span>
                <span className={styles.version}>{c.version}</span>
                <span className={styles.license}>{c.license}</span>
              </summary>
              <div className={styles.body}>
                <p>
                  {t.source}: <a href={c.url}>{c.url.replace(/^https?:\/\//, '')}</a>
                </p>
                {c.text ? <pre>{c.text}</pre> : <p>{format(t.noText, { license: c.license })}</p>}
              </div>
            </details>
          </li>
        ))}
      </ul>
    </section>
  );
}

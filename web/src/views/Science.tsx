import { PageShell } from '@/components/chrome';
import { Mark } from '@/components/Mark';
import { Prose, mdxComponents } from '@/components/prose';
import { Container, Label } from '@/components/ui';
import { loadMdx } from '@/lib/content';
import { format, intlLocale, messages, type Locale } from '@/lib/i18n';
import { pageMetadata } from '@/lib/metadata';
import styles from './Science.module.css';

export type ScienceFrontMatter = { title: string; description: string; model: string; checked: string };

const SECTIONS = [
  'sources',
  'baselines',
  'recovery',
  'readiness',
  'strain',
  'sleep',
  'journal',
  'versions',
  'limitations',
] as const;

export async function scienceMetadata(locale: Locale) {
  const { data } = await loadMdx<ScienceFrontMatter>(`science/${locale}.mdx`);
  return pageMetadata({ locale, path: '/science', title: data.title, description: data.description });
}

export async function SciencePage({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const { data, Content } = await loadMdx<ScienceFrontMatter>(`science/${locale}.mdx`);
  const checked = new Intl.DateTimeFormat(intlLocale(locale), { dateStyle: 'long', timeZone: 'UTC' }).format(
    new Date(`${data.checked}T00:00:00Z`),
  );
  return (
    <PageShell locale={locale} path="/science">
      <Container className={styles.layout}>
        <header className={styles.header}>
          <Label tone="accent">{t.science.label}</Label>
          <h1>{data.title}</h1>
          <p className={styles.meta}>
            <Mark size={16} />
            {format(t.science.model, { model: data.model, date: checked })}
          </p>
        </header>
        <nav aria-labelledby="toc-title" className={styles.toc}>
          <Label as="h2" id="toc-title">
            {t.science.onThisPage}
          </Label>
          <ol>
            {SECTIONS.map((id) => (
              <li key={id}>
                <a href={`#${id}`}>{t.science.toc[id]}</a>
              </li>
            ))}
          </ol>
        </nav>
        <article className={styles.article}>
          <Prose>
            <Content components={mdxComponents} />
          </Prose>
          <p className={styles.wellness}>{t.home.scores.disclaimer}</p>
        </article>
      </Container>
    </PageShell>
  );
}

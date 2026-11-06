import { PageShell } from '@/components/chrome';
import { MetricIcon } from '@/components/charts';
import { Mark } from '@/components/Mark';
import { SleepScreen, StrainScreen, TodayScreen } from '@/components/Mockups';
import { ButtonLink, Container, Eyebrow, Marquee, Section } from '@/components/ui';
import { format, localePath, messages, type Locale } from '@/lib/i18n';
import { BUILD_YEAR, SUPPORT_EMAIL, TESTFLIGHT_URL } from '@/lib/site';
import styles from './Home.module.css';

const SCORE_IDS = ['readiness', 'recovery', 'strain', 'sleep'] as const;
const SYSTEM_IDS = ['recovery', 'sleep', 'strain'] as const;

export function HomePage({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const h = t.home;
  const link = (path: string) => localePath(locale, path);
  const mailto = SUPPORT_EMAIL
    ? `mailto:${SUPPORT_EMAIL}?subject=${encodeURIComponent(t.beta.inviteSubject)}&body=${encodeURIComponent(t.beta.inviteBody)}`
    : null;
  return (
    <PageShell locale={locale} path="/">
      <Hero locale={locale} />

      <Marquee text={h.ticker} />

      <Container>
        <section className={styles.worksWith} aria-labelledby="works-title">
          <Eyebrow id="works-title" as="h2">
            {h.worksWith.label}
          </Eyebrow>
          {/* A statement of the data source, not a row of names: the watches and rings are
              where the data comes from, not partners (web D23). */}
          <p>{h.worksWith.text}</p>
        </section>
      </Container>

      <Container>
        <Section
          id="scores"
          number={h.scores.number}
          label={h.scores.label}
          title={h.scores.title}
          intro={<p>{h.scores.intro}</p>}
        >
          <div>
            <ul className={styles.specs}>
              {SCORE_IDS.map((id, i) => {
                const s = h.scores.items[id];
                return (
                  <li key={id} className={styles.spec}>
                    <span className={styles.specIndex} aria-hidden="true">
                      {String(i + 1).padStart(2, '0')}
                    </span>
                    <span className={styles.specIcon} aria-hidden="true">
                      {id === 'readiness' ? <Mark size={22} /> : <MetricIcon kind={id} />}
                    </span>
                    <div className={styles.specText}>
                      <h3>{s.name}</h3>
                      <p>{s.measures}</p>
                    </div>
                    <p className={styles.specValue}>
                      <span className="visually-hidden">{h.scores.sample}: </span>
                      {s.value}
                      <span className={styles.specRange}> / {s.range}</span>
                    </p>
                    <a className={styles.specMore} href={`${link('/science')}#${id}`}>
                      {h.scores.more}
                      <span className="visually-hidden">: {s.name}</span>
                      <span aria-hidden="true"> ↗</span>
                    </a>
                  </li>
                );
              })}
            </ul>
            <p className={styles.caption}>{h.scores.sampleNote}</p>
          </div>
          <p className={styles.disclaimer}>
            <Mark size={16} />
            <span>{h.scores.disclaimer}</span>
          </p>
        </Section>

        <Section id="systems" number={h.systems.number} label={h.systems.label} title={h.systems.title}>
          <div className={styles.systems}>
            {SYSTEM_IDS.map((id, i) => {
              const s = h.systems.items[id];
              return (
                <article key={id} className={styles.system} aria-labelledby={`system-${id}`}>
                  <div className={styles.systemText}>
                    <p className={styles.systemIndex} aria-hidden="true">
                      {String(i + 1).padStart(2, '0')}
                    </p>
                    <Eyebrow>{s.kicker}</Eyebrow>
                    <h3 id={`system-${id}`}>{s.title}</h3>
                    <p>{s.body}</p>
                  </div>
                  <figure className={styles.systemFigure}>
                    {id === 'recovery' && <TodayScreen labels={h.mock.today} />}
                    {id === 'sleep' && <SleepScreen labels={h.mock.sleep} />}
                    {id === 'strain' && <StrainScreen labels={h.mock.strain} />}
                    <figcaption className={styles.caption}>{s.figure}</figcaption>
                  </figure>
                </article>
              );
            })}
          </div>
        </Section>

        <Section
          id="principles"
          number={h.principles.number}
          label={h.principles.label}
          title={h.principles.title}
        >
          <ol className={styles.principles}>
            {h.principles.items.map((p, i) => (
              <li key={p.title}>
                <span className={styles.principleIndex} aria-hidden="true">
                  {String(i + 1).padStart(2, '0')}
                </span>
                <h3>{p.title}</h3>
                <p>{p.body}</p>
              </li>
            ))}
          </ol>
        </Section>

        <Section id="how" number={h.start.number} label={h.start.label} title={h.start.title}>
          <ol className={styles.steps}>
            {h.start.steps.map((step, i) => (
              <li key={step.title}>
                <span className={styles.stepIndex} aria-hidden="true">
                  {String(i + 1).padStart(2, '0')}
                </span>
                <h3>{step.title}</h3>
                <p>{step.body}</p>
              </li>
            ))}
          </ol>
        </Section>
      </Container>

      <section className={styles.facts} aria-label={h.facts.label}>
        <Container>
          <dl className={styles.factGrid}>
            {h.facts.items.map(([value, label]) => (
              <div key={label} className={styles.fact}>
                <dt>{label}</dt>
                <dd>{value}</dd>
              </div>
            ))}
          </dl>
        </Container>
      </section>

      <Container>
        <Section id="privacy" number={h.privacy.number} label={h.privacy.label} title={h.privacy.title}>
          <ul className={styles.privacy}>
            {h.privacy.items.map((f) => (
              <li key={f.title}>
                <h3>{f.title}</h3>
                <p>{f.body}</p>
              </li>
            ))}
          </ul>
          <p className={styles.sectionAction}>
            <ButtonLink href={link('/privacy')} variant="secondary" arrow>
              {h.privacy.link}
            </ButtonLink>
          </p>
        </Section>

        <Section id="pricing" number={h.pricing.number} label={h.pricing.label} title={h.pricing.title}>
          <Pricing locale={locale} />
        </Section>

        <Section id="faq" number={h.faq.number} label={h.faq.label} title={h.faq.title}>
          <div className={styles.faq}>
            {h.faq.items.map((item) => (
              <details key={item.q} className={styles.question}>
                <summary>
                  <h3>{item.q}</h3>
                  <span className={styles.plus} aria-hidden="true" />
                </summary>
                <p>{item.a}</p>
              </details>
            ))}
          </div>
        </Section>
      </Container>

      <section className={styles.download} aria-labelledby="download-title">
        <Container className={styles.downloadGrid}>
          <div className={styles.downloadText}>
            <Eyebrow tone="inverse">
              {h.download.number} / {h.download.label}
            </Eyebrow>
            <h2 id="download-title">{h.download.title}</h2>
            <p>{h.download.body}</p>
            <div className={styles.downloadActions}>
              {TESTFLIGHT_URL && (
                <ButtonLink href={TESTFLIGHT_URL} variant="inverse" arrow rel="noopener">
                  {h.download.testflight}
                </ButtonLink>
              )}
              {mailto && (
                <ButtonLink href={mailto} variant="inverseOutline" arrow>
                  {h.download.invite}
                </ButtonLink>
              )}
              <ButtonLink
                href={link('/beta')}
                variant={TESTFLIGHT_URL || mailto ? 'inverseOutline' : 'inverse'}
                arrow
              >
                {h.download.how}
              </ButtonLink>
            </div>
          </div>
          <figure className={styles.downloadFigure}>
            <SleepScreen labels={h.mock.sleep} />
            <figcaption className={`${styles.caption} ${styles.captionInverse}`}>
              {h.download.figure}
            </figcaption>
          </figure>
        </Container>
      </section>
    </PageShell>
  );
}

function Hero({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const h = t.home.hero;
  return (
    <section className={styles.hero} aria-labelledby="hero-title">
      <div className={styles.guides} aria-hidden="true">
        <span />
        <span />
        <span />
        <span />
      </div>
      <Container className={styles.heroInner}>
        <p className={styles.meta}>
          <span>{h.metaLeft}</span>
          <span>{format(h.metaCenter, { year: BUILD_YEAR })}</span>
          <span>{h.metaRight}</span>
        </p>
        <h1 id="hero-title" className={styles.title}>
          <span>{h.title[0]}</span> <span>{h.title[1]}</span>
        </h1>
        <div className={styles.heroGrid}>
          <div className={styles.heroText}>
            <p className={styles.lead}>{h.lead}</p>
            <div className={styles.actions}>
              <ButtonLink href={localePath(locale, '/beta')} arrow>
                {h.primary}
              </ButtonLink>
              <ButtonLink href="#how" variant="secondary">
                {h.secondary}
              </ButtonLink>
            </div>
            <ul className={styles.heroFacts}>
              {h.facts.map((f) => (
                <li key={f}>{f}</li>
              ))}
            </ul>
          </div>
          <figure className={styles.heroFigure}>
            <TodayScreen labels={t.home.mock.today} />
            <figcaption className={styles.caption}>{h.figure}</figcaption>
          </figure>
        </div>
      </Container>
    </section>
  );
}

/**
 * One plan: free during the beta, as the terms in force say. No price, no paid tier: later
 * plans are announced here first (web D23).
 */
function Pricing({ locale }: { locale: Locale }) {
  const p = messages(locale).home.pricing;
  return (
    <div className={styles.plans}>
      <article className={styles.plan} aria-labelledby="plan-free">
        <h3 id="plan-free">{p.free.name}</h3>
        <p className={styles.planLead}>{p.free.lead}</p>
        <ul className={styles.planItems}>
          {p.free.items.map((i) => (
            <li key={i}>{i}</li>
          ))}
        </ul>
        <ButtonLink href={localePath(locale, '/beta')} arrow className={styles.planCta}>
          {p.free.cta}
        </ButtonLink>
      </article>
      <p className={styles.pricingNote}>{p.note}</p>
    </div>
  );
}

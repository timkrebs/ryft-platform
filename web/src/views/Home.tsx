import { Gauge, MetricIcon, TrendChart } from '@/components/charts';
import { PageShell } from '@/components/chrome';
import { Mark } from '@/components/Mark';
import { DeviceFrame, Screenshot } from '@/components/Screenshot';
import { ButtonLink, Container, Label, SampleNote, Section } from '@/components/ui';
import { format, intlLocale, localePath, messages, type Locale } from '@/lib/i18n';
import styles from './Home.module.css';

// Sample numbers for the hero, the same ones the app's previews and brand board use.
const SAMPLE = { readiness: 84, recovery: 0.82, strain: 12.4, week: [71, 76, 68, 79, 74, 81, 84] };
const SCORE_IDS = ['readiness', 'recovery', 'strain', 'sleep'] as const;

export function HomePage({ locale }: { locale: Locale }) {
  const t = messages(locale).home;
  const link = (path: string) => localePath(locale, path);
  return (
    <PageShell locale={locale} path="/">
      <Hero locale={locale} />
      <Container>
        <Section id="how" label={t.how.label} title={t.how.title} intro={<p>{t.how.intro}</p>}>
          <ol className={styles.steps}>
            {t.how.steps.map((step, i) => (
              <li key={step.title} className={styles.step}>
                <span className={styles.stepIcon} aria-hidden="true">
                  <StepGlyph index={i} />
                </span>
                <span className={styles.stepNumber} aria-hidden="true">
                  {String(i + 1).padStart(2, '0')}
                </span>
                <h3>{step.title}</h3>
                <p>{step.body}</p>
              </li>
            ))}
          </ol>
        </Section>

        <Section id="scores" label={t.scores.label} title={t.scores.title} intro={<p>{t.scores.intro}</p>}>
          <div className={styles.scores}>
            {SCORE_IDS.map((id) => (
              <ScoreCard key={id} locale={locale} id={id} />
            ))}
          </div>
          <div className={styles.phones}>
            <figure className={styles.phone}>
              <DeviceFrame>
                <Screenshot id="sleep" alt={t.scores.shots.sleep} sizes="(min-width: 48rem) 300px, 45vw" />
              </DeviceFrame>
            </figure>
            <figure className={styles.phone}>
              <DeviceFrame>
                <Screenshot id="strain" alt={t.scores.shots.strain} sizes="(min-width: 48rem) 300px, 45vw" />
              </DeviceFrame>
            </figure>
            <p className={styles.disclaimer}>
              <Mark size={18} />
              <span>{t.scores.disclaimer}</span>
            </p>
          </div>
        </Section>

        <Section id="features" label={t.features.label} title={t.features.title}>
          <div className={styles.features}>
            <article className={styles.feature}>
              <div className={styles.panel}>
                <Screenshot
                  id="train-session"
                  alt={t.features.train.alt}
                  sizes="(min-width: 64rem) 340px, (min-width: 40rem) 45vw, 90vw"
                />
              </div>
              <h3>{t.features.train.title}</h3>
              <p>{t.features.train.body}</p>
            </article>
            <article className={styles.feature}>
              <JournalSample locale={locale} />
              <h3>{t.features.journal.title}</h3>
              <p>{t.features.journal.body}</p>
            </article>
            <article className={styles.feature}>
              <div className={styles.panel}>
                <Screenshot
                  id="insights-cards"
                  alt={t.features.insights.alt}
                  sizes="(min-width: 64rem) 340px, (min-width: 40rem) 45vw, 90vw"
                />
              </div>
              <h3>{t.features.insights.title}</h3>
              <p>{t.features.insights.body}</p>
            </article>
          </div>
        </Section>

        <Section id="principles" label={t.principles.label} title={t.principles.title}>
          <ol className={styles.principles}>
            {t.principles.items.map((p, i) => (
              <li key={p.title}>
                <span className={styles.principleNumber} aria-hidden="true">
                  {String(i + 1).padStart(2, '0')}
                </span>
                <h3>{p.title}</h3>
                <p>{p.body}</p>
              </li>
            ))}
          </ol>
        </Section>

        <Section id="privacy" label={t.privacy.label} title={t.privacy.title}>
          <ul className={styles.facts}>
            {t.privacy.items.map((f) => (
              <li key={f.title}>
                <h3>{f.title}</h3>
                <p>{f.body}</p>
              </li>
            ))}
          </ul>
          <p>
            <ButtonLink href={link('/privacy')} variant="secondary">
              {t.privacy.link}
            </ButtonLink>
          </p>
        </Section>

        <Section id="faq" label={t.faq.label} title={t.faq.title}>
          <div className={styles.faq}>
            {t.faq.items.map((item) => (
              <details key={item.q} className={styles.question}>
                <summary>
                  <span>{item.q}</span>
                </summary>
                <p>{item.a}</p>
              </details>
            ))}
          </div>
        </Section>

        <section className={styles.cta} aria-labelledby="cta-title">
          <Mark size={40} />
          <h2 id="cta-title">{t.cta.title}</h2>
          <p>{t.cta.body}</p>
          <ButtonLink href={link('/beta')}>{t.cta.button}</ButtonLink>
        </section>
      </Container>
    </PageShell>
  );
}

function Hero({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const h = t.home.hero;
  const today = t.home.today;
  const number = new Intl.NumberFormat(intlLocale(locale), { maximumFractionDigits: 1 });
  const percent = new Intl.NumberFormat(intlLocale(locale), { style: 'percent' });
  const ticks = today.weekdays.split(',');
  return (
    <Container className={styles.hero}>
      <div className={styles.heroText}>
        <Label tone="accent">{h.eyebrow}</Label>
        <h1>{h.title}</h1>
        <p className={styles.lead}>{h.lead}</p>
        <div className={styles.actions}>
          <ButtonLink href={localePath(locale, '/beta')}>{h.primary}</ButtonLink>
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
      <figure className={styles.today}>
        <DeviceFrame className={styles.todayFrame}>
          <div className={styles.todayScreen}>
            <p className={styles.greeting}>{today.greeting}</p>
            <p className={styles.state}>{today.state}</p>
            <Gauge value={SAMPLE.readiness} label={today.readiness} />
            <ul className={styles.tiles}>
              <li>
                <span className={styles.tileLabel}>{today.sleep}</span>
                <span className={styles.tileValue}>{today.sleepValue}</span>
                <MetricIcon kind="sleep" />
              </li>
              <li>
                <span className={styles.tileLabel}>{today.recovery}</span>
                <span className={styles.tileValue}>{percent.format(SAMPLE.recovery)}</span>
                <MetricIcon kind="recovery" />
              </li>
              <li>
                <span className={styles.tileLabel}>{today.strain}</span>
                <span className={styles.tileValue}>{number.format(SAMPLE.strain)}</span>
                <MetricIcon kind="strain" />
              </li>
            </ul>
            <div className={styles.trendCard}>
              <p className={styles.trendHeader}>
                <span>{today.trendTitle}</span>
                <span>{today.trendWindow}</span>
              </p>
              <TrendChart
                values={SAMPLE.week}
                ticks={ticks}
                label={format(today.chartLabel, {
                  values: SAMPLE.week.slice(0, -1).join(', '),
                  today: SAMPLE.week[SAMPLE.week.length - 1],
                })}
              />
            </div>
          </div>
        </DeviceFrame>
        <figcaption>
          <SampleNote>{today.sample}</SampleNote>
        </figcaption>
      </figure>
    </Container>
  );
}

function ScoreCard({ locale, id }: { locale: Locale; id: (typeof SCORE_IDS)[number] }) {
  const t = messages(locale).home.scores;
  const s = t.items[id];
  const weighted = id === 'recovery' || id === 'readiness';
  const tones = s.bands.length === 4 ? ['ramp1', 'ramp2', 'ramp3', 'ramp4'] : ['good', 'mid', 'low'];
  return (
    <article className={styles.score} aria-labelledby={`score-${id}`}>
      <header className={styles.scoreHeader}>
        <h3 id={`score-${id}`}>{s.name}</h3>
        <p className={styles.range}>
          <span className="visually-hidden">{t.range}: </span>
          {s.range}
        </p>
      </header>
      <p className={styles.measures}>{s.measures}</p>
      <div>
        <Label as="h3" className={styles.scoreLabel}>
          {t.inputs}
        </Label>
        <ul className={styles.inputs}>
          {s.inputs.map(([name, weight]) => (
            <li key={name}>
              <span>{name}</span>
              <span className={styles.weight}>{weight}</span>
              {weighted && (
                <svg viewBox="0 0 100 2" preserveAspectRatio="none" aria-hidden="true" className={styles.bar}>
                  <rect width="100" height="2" className={styles.barTrack} />
                  <rect width={Number.parseFloat(weight)} height="2" className={styles.barFill} />
                </svg>
              )}
            </li>
          ))}
        </ul>
        <p className={styles.note}>{s.note}</p>
      </div>
      <div>
        <Label as="h3" className={styles.scoreLabel}>
          {t.bands}
        </Label>
        <dl className={styles.bands}>
          {s.bands.map(([range, word], i) => (
            <div key={range} className={styles.band}>
              <dt>
                <span className={`${styles.dot} ${styles[`dot-${tones[i]}`]}`} />
                {range}
              </dt>
              <dd>{word}</dd>
            </div>
          ))}
        </dl>
      </div>
      <p className={styles.more}>
        <a href={`${localePath(locale, '/science')}#${id}`}>
          {t.more}
          <span className="visually-hidden">: {s.name}</span> →
        </a>
      </p>
    </article>
  );
}

function JournalSample({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const j = t.home.features.journal;
  return (
    <figure className={`${styles.panel} ${styles.journal}`}>
      <div className={styles.journalCard}>
        <Label tone="warning">{j.card}</Label>
        <p className={styles.journalBehavior}>{j.behavior}</p>
        <p className={styles.journalEffect}>{j.effect}</p>
        {/* −16 … +4 points across 200 px: zero at 160, the interval −12.1 … −4.6, the mean −8.4. */}
        <svg viewBox="0 0 200 24" className={styles.interval} aria-hidden="true">
          <line x1="0" x2="200" y1="12" y2="12" className={styles.intervalAxis} strokeWidth="1" />
          <line
            x1="160"
            x2="160"
            y1="3"
            y2="21"
            className={styles.intervalZero}
            strokeWidth="1"
            strokeDasharray="2 2"
          />
          <line
            x1="39"
            x2="114"
            y1="12"
            y2="12"
            className={styles.intervalRange}
            strokeWidth="4"
            strokeLinecap="round"
          />
          <circle cx="76" cy="12" r="5" className={styles.intervalPoint} />
        </svg>
        <p className={styles.journalMeta}>{j.interval}</p>
        <p className={styles.journalMeta}>{j.days}</p>
      </div>
      <figcaption>
        <SampleNote>{t.a11y.sampleData}</SampleNote>
      </figcaption>
    </figure>
  );
}

function StepGlyph({ index }: { index: number }) {
  if (index === 1) return <Mark size={28} />;
  if (index === 0) {
    return (
      <svg viewBox="0 0 28 28" width="28" height="28" focusable="false">
        <path
          d="M2 15h6l2.5-6 4 12 3-9 2 3H26"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    );
  }
  return (
    <svg viewBox="0 0 28 28" width="28" height="28" focusable="false">
      <circle cx="14" cy="14" r="11" fill="none" stroke="currentColor" strokeOpacity="0.3" strokeWidth="2" />
      <path
        d="M14 3a11 11 0 1 1-10.4 7.4"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
      />
    </svg>
  );
}

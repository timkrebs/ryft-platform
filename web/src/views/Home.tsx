import { PageShell } from '@/components/chrome';
import { Check, MoonGlyph, PulseGlyph } from '@/components/icons';
import { InsightCard } from '@/components/insight';
import { DotRow, GlowLabel, GlowTile, OrbitGauge, Ruler, Sparkline } from '@/components/instruments';
import { Mark } from '@/components/Mark';
import { DeviceFrame } from '@/components/Screenshot';
import {
  ButtonLink,
  Card,
  Container,
  Group,
  GroupRow,
  IconDisc,
  Label,
  SampleNote,
  Section,
} from '@/components/ui';
import { localePath, messages, type Locale } from '@/lib/i18n';
import styles from './Home.module.css';

// Sample numbers for the hero: the ones the app's previews and Today board use.
const SAMPLE = { readiness: 84, recovery: 82, sleep: 94, strain: 12.4, hrv: [14, 6, 12, 5, 10, 3, 8] };
const SCORE_IDS = ['readiness', 'recovery', 'strain', 'sleep'] as const;
// The three steps' glyphs: the watch's pulse, RYFT's orbit mark, the night.
const STEP_GLYPHS = [<PulseGlyph key="pulse" />, <Mark key="mark" size={18} />, <MoonGlyph key="moon" />];

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
              <li key={step.title}>
                <Card as="div" className={styles.step}>
                  <IconDisc>{STEP_GLYPHS[i]}</IconDisc>
                  <h3>{step.title}</h3>
                  <p>{step.body}</p>
                </Card>
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
          <p className={styles.disclaimer}>
            <Mark size={18} />
            <span>{t.scores.disclaimer}</span>
          </p>
        </Section>

        <Section id="features" label={t.features.label} title={t.features.title}>
          <div className={styles.features}>
            <article className={styles.feature}>
              <TrainSample locale={locale} />
              <h3>{t.features.train.title}</h3>
              <p>{t.features.train.body}</p>
            </article>
            <article className={styles.feature}>
              <JournalSample locale={locale} />
              <h3>{t.features.journal.title}</h3>
              <p>{t.features.journal.body}</p>
            </article>
            <article className={styles.feature}>
              <InsightsSample locale={locale} />
              <h3>{t.features.insights.title}</h3>
              <p>{t.features.insights.body}</p>
            </article>
          </div>
        </Section>

        <Section id="principles" label={t.principles.label} title={t.principles.title}>
          <Group as="ol" className={styles.principles}>
            {t.principles.items.map((p) => (
              <GroupRow key={p.title}>
                <h3>{p.title}</h3>
                <p>{p.body}</p>
              </GroupRow>
            ))}
          </Group>
        </Section>

        <Section id="privacy" label={t.privacy.label} title={t.privacy.title}>
          <ul className={styles.facts}>
            {t.privacy.items.map((f) => (
              <li key={f.title}>
                <Card className={styles.fact}>
                  <h3>{f.title}</h3>
                  <p>{f.body}</p>
                </Card>
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
          <Group as="div" className={styles.faq}>
            {t.faq.items.map((item) => (
              <details key={item.q} className={styles.question}>
                <summary>
                  <span>{item.q}</span>
                </summary>
                <p>{item.a}</p>
              </details>
            ))}
          </Group>
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

/** The hero: the title and the two ways in, and Today drawn the way the app draws it (sample data). */
function Hero({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const h = t.home.hero;
  const today = t.home.today;
  return (
    <Container className={styles.hero}>
      <div className={styles.heroText}>
        <Label>{h.eyebrow}</Label>
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
            <div className={styles.todayHead}>
              <p className={styles.greeting}>{today.greeting}</p>
              <p className={styles.state}>{today.state}</p>
            </div>
            <OrbitGauge
              readiness={SAMPLE.readiness}
              recovery={SAMPLE.recovery}
              sleep={SAMPLE.sleep}
              strain={SAMPLE.strain}
              label={`${today.readiness}: ${SAMPLE.readiness}`}
            />
            <p className={styles.band}>
              <span>{today.readinessBand}</span>
              <span className={styles.delta}>
                <svg viewBox="0 0 10 9" width="10" height="9" aria-hidden="true" focusable="false">
                  <path d="M5 0 10 9H0Z" />
                </svg>
                {today.delta}
              </span>
            </p>
            <Ruler
              min={0}
              max={100}
              value={SAMPLE.readiness}
              zone={[67, 100]}
              ticks={61}
              labels={today.scale}
            />
            <div className={styles.tiles}>
              <GlowTile metric="recovery">
                <div className={styles.tileHead}>
                  <GlowLabel>{today.recovery}</GlowLabel>
                  <Sparkline values={SAMPLE.hrv} />
                </div>
                <p className={styles.tileValue}>
                  <span className={styles.number}>{SAMPLE.recovery}</span>
                  <span className={styles.unit}>%</span>
                  <span className={styles.tileBand}>{today.recoveryBand}</span>
                </p>
              </GlowTile>
              <GlowTile metric="sleep">
                <div className={styles.tileHead}>
                  <GlowLabel>{today.sleep}</GlowLabel>
                  <DotRow marks={['full', 'missed', 'noData', 'full', 'missed', 'full', 'today']} />
                </div>
                <p className={styles.tileValue}>
                  <span className={styles.number}>{today.sleepValue}</span>
                  <span className={styles.unit}>{today.sleepUnit}</span>
                </p>
              </GlowTile>
            </div>
            <GlowTile metric="strain">
              <div className={styles.strainRow}>
                <GlowLabel>{today.strain}</GlowLabel>
                <p className={styles.tileValue}>
                  <span className={styles.number}>{SAMPLE.strain}</span>
                  <span className={styles.unit}>{today.strainNote}</span>
                </p>
              </div>
              <Ruler
                min={0}
                max={21}
                value={SAMPLE.strain}
                zone={[10, 14]}
                ticks={43}
                tint="strain"
                compact
              />
            </GlowTile>
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
  // The legend: readiness and recovery bands are recovery's teal, amber and coral; sleep and strain
  // are their own colour at rising strength (brief §1, "Die Ringe sind die Legende").
  const tones =
    id === 'strain'
      ? ['strain1', 'strain2', 'strain3', 'strain4']
      : id === 'sleep'
        ? ['sleep1', 'sleep2', 'sleep3']
        : ['good', 'mid', 'low'];
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

/** Train: the rest timer and a set list, as the app draws them (sample data). */
function TrainSample({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const s = t.home.features.train.sample;
  const sets = [
    { n: 1, kg: 80, reps: 5, done: true },
    { n: 2, kg: 80, reps: 5, done: true },
    { n: 3, kg: 80, reps: 5, done: false },
  ];
  return (
    <figure className={styles.panel}>
      <Card className={styles.rest}>
        <Label>{s.rest}</Label>
        <p className={styles.restTime}>{s.time}</p>
        <p className={styles.caption}>{s.next}</p>
        <p className={styles.restActions} aria-hidden="true">
          <span className={styles.chip}>{s.back}</span>
          <span className={styles.chip}>{s.forward}</span>
          <span className={styles.skip}>{s.skip}</span>
        </p>
      </Card>
      <Card className={styles.sets}>
        <div className={styles.exercise}>
          <p className={styles.exerciseName}>{s.exercise}</p>
          <p className={styles.caption}>{s.plan}</p>
        </div>
        <table className={styles.setTable}>
          <thead>
            <tr>
              <th scope="col">{s.set}</th>
              <th scope="col">{s.kg}</th>
              <th scope="col">{s.reps}</th>
              <td />
            </tr>
          </thead>
          <tbody>
            {sets.map((row) => (
              <tr key={row.n}>
                <th scope="row">{row.n}</th>
                <td>{row.kg}</td>
                <td>{row.reps}</td>
                <td>
                  <span className={row.done ? styles.checkDone : styles.checkOpen} aria-hidden="true">
                    <Check size={14} />
                  </span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Card>
      <figcaption>
        <SampleNote>{t.a11y.sampleData}</SampleNote>
      </figcaption>
    </figure>
  );
}

function JournalSample({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const j = t.home.features.journal;
  return (
    <figure className={styles.panel}>
      <Card className={styles.journalCard}>
        <p className={styles.journalWord}>
          <span className={styles.journalDot} aria-hidden="true" />
          {j.card}
        </p>
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
      </Card>
      <figcaption>
        <SampleNote>{t.a11y.sampleData}</SampleNote>
      </figcaption>
    </figure>
  );
}

/** Insights: the cards as the app draws them, the first of them the recovery glow (sample data). */
function InsightsSample({ locale }: { locale: Locale }) {
  const t = messages(locale);
  const cards = t.home.features.insights.cards;
  const severities = ['warn', 'notice', 'info'] as const;
  return (
    <figure className={styles.panel}>
      {cards.map((c, i) => (
        <InsightCard
          key={c.title}
          severity={severities[i]}
          word={c.word}
          title={c.title}
          body={c.body}
          glow={i === cards.length - 1}
        />
      ))}
      <figcaption>
        <SampleNote>{t.a11y.sampleData}</SampleNote>
      </figcaption>
    </figure>
  );
}

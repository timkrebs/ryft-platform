import tokens from '@/styles/tokens.json';
import { Logo, Lockup, Wordmark } from '@/components/Brand';
import { MoonGlyph } from '@/components/icons';
import { InsightCard } from '@/components/insight';
import { DotNumeral, DotRow, GlowLabel, GlowTile, OrbitGauge, Ruler } from '@/components/instruments';
import { Mark } from '@/components/Mark';
import { PageShell } from '@/components/chrome';
import {
  ButtonLink,
  Card,
  Container,
  Group,
  GroupLink,
  GroupRow,
  IconDisc,
  Label,
  SampleNote,
  Section,
  Tile,
  Todo,
} from '@/components/ui';
import '@/styles/swatches.css';
import styles from './Styleguide.module.css';

const typeSamples: [keyof typeof tokens.type.scale, string][] = [
  ['display', '84'],
  ['number-large', '7:48'],
  ['title', 'Good morning'],
  ['metric', '82 %'],
  ['headline', 'HRV above your baseline'],
  ['body', 'Your HRV is 12 % above your 30-day average.'],
  ['callout', 'Quality is 60 % performance, 20 % efficiency, 20 % restorative.'],
  ['caption', 'Model 2026.10.1'],
  ['label', 'Overall readiness'],
  ['axis', '0 low moderate high 100'],
];

const SCALES = {
  readiness: ['0', 'low', 'moderate', 'high', '100'],
  sleep: ['0 h', 'need 8:18', '10 h'],
  strain: ['0', 'target 10–14', '21'],
};

const INSIGHTS = [
  { severity: 'warn', word: 'Attention', title: 'Strain outpacing recovery', body: 'Plan a lighter day.' },
  { severity: 'notice', word: 'Notice', title: 'Irregular sleep times', body: 'Keep your bedtime steady.' },
  { severity: 'info', word: 'Insight', title: 'HRV above your baseline', body: 'Your highest in a month.' },
] as const;

export function StyleguidePage() {
  const colors = Object.entries(tokens.colors);
  return (
    <PageShell locale="en" path="/styleguide">
      <Container>
        <Section
          headingLevel={1}
          label="Internal"
          title="Styleguide"
          intro={<p>Generated from the app: {tokens.source}. Nothing here is hard-coded.</p>}
        />

        <Section id="mark" title="Mark and wordmark">
          <div className={styles.row}>
            <Mark size={120} />
            <Mark size={48} />
            <Mark size={32} />
            <Mark size={24} />
            <Mark size={18} />
            <Mark size={48} showsInnerOrbit={false} />
          </div>
          <div className={styles.row}>
            <Logo />
            <Logo size={44} />
            <Wordmark />
          </div>
          <Lockup tagline="Your whole health." />
        </Section>

        <Section id="colors" title="Colours" intro={<p>{colors.length} colour sets from Assets.xcassets.</p>}>
          <ul className={styles.swatches}>
            {colors.map(([name, value]) => (
              <li key={name} className={styles.swatch}>
                <span className={`${styles.chip} swatch-${name}`} aria-hidden="true" />
                <span className={styles.swatchName}>--{name}</span>
                <span className={styles.swatchValue}>{value.dark}</span>
              </li>
            ))}
          </ul>
        </Section>

        <Section
          id="type"
          title="Type"
          intro={<p>Satoshi in the app scale: Light titles and numbers, Regular text, Medium buttons.</p>}
        >
          <dl className={styles.type}>
            {typeSamples.map(([name, sample]) => (
              <div key={name} className={styles.typeRow}>
                <dt>
                  {name} · {tokens.type.scale[name].pt} pt · {tokens.type.scale[name].weight}
                </dt>
                <dd className={styles[`type-${name}`]}>{sample}</dd>
              </div>
            ))}
          </dl>
        </Section>

        <Section id="buttons" title="Buttons and links">
          <div className={styles.row}>
            <ButtonLink href="#buttons">Join the beta</ButtonLink>
            <ButtonLink href="#buttons" icon="out">
              Open TestFlight
            </ButtonLink>
            <ButtonLink href="#buttons" variant="secondary">
              How it works
            </ButtonLink>
            <ButtonLink href="#buttons" variant="quiet">
              Quiet link
            </ButtonLink>
          </div>
          <p>
            A link in running text is <a href="#buttons">ink with an underline</a>, never colour alone.
          </p>
          <div className={styles.row}>
            <Label>Secondary</Label>
            <Label tone="tertiary">Tertiary</Label>
            <Label tone="warning">Notice</Label>
            <Label tone="alert">Attention</Label>
          </div>
        </Section>

        <Section id="surfaces" title="Cards, tiles and lists">
          <div className={styles.grid}>
            <Card>
              <Label>Card</Label>
              <p className={styles.cardTitle}>Radius 22, padding 16</p>
              <p className={styles.muted}>The app&apos;s neutral card on the surface colour.</p>
            </Card>
            <Tile label="Steps" value="9,412" />
            <Tile label="Active energy" value="612" unit="kcal" />
            <Card className={styles.disc}>
              <IconDisc>
                <MoonGlyph />
              </IconDisc>
              <p>Icon disc: a thin glyph in a faint ring.</p>
            </Card>
          </div>
          <div className={styles.grid}>
            <Group title="About you" footer="A footer is 12 pt, tertiary.">
              <GroupRow>Birth year</GroupRow>
              <GroupRow>Baseline sleep need</GroupRow>
              <GroupLink href="#surfaces" detail="Learned">
                Maximum heart rate
              </GroupLink>
              <GroupLink href="#surfaces" out>
                Open Health
              </GroupLink>
            </Group>
            <div className={styles.stack}>
              {INSIGHTS.map((card, i) => (
                <InsightCard key={card.title} {...card} glow={i === INSIGHTS.length - 1} />
              ))}
            </div>
          </div>
          <Card>
            <p>
              A draft value: <Todo>TODO(user): support email address</Todo>
            </p>
            <SampleNote>Sample data</SampleNote>
          </Card>
        </Section>

        <Section
          id="glow"
          title="Glow tiles"
          intro={<p>One per section: the metric colour under a dark core, text in the core.</p>}
        >
          <div className={styles.grid}>
            <GlowTile metric="recovery">
              <GlowLabel>Recovery</GlowLabel>
              <p className={styles.metric}>82 %</p>
            </GlowTile>
            <GlowTile metric="sleep">
              <GlowLabel>Sleep</GlowLabel>
              <p className={styles.metric}>7:48 h</p>
            </GlowTile>
            <GlowTile metric="strain">
              <GlowLabel>Strain</GlowLabel>
              <p className={styles.metric}>12.4</p>
            </GlowTile>
          </div>
        </Section>

        <Section id="instruments" title="Numerals, rulers and the gauge">
          <div className={styles.row}>
            <DotNumeral text="84" label="84" />
            <DotNumeral text="7:48" label="7:48" glyphGap={7} />
            <DotNumeral text="12.4" label="12.4" glyphGap={7} />
            <DotRow marks={['full', 'missed', 'noData', 'full', 'full', 'missed', 'today']} />
          </div>
          <div className={styles.rulers}>
            <Ruler min={0} max={100} value={84} zone={[67, 100]} ticks={61} labels={SCALES.readiness} />
            <Ruler
              min={0}
              max={10}
              value={7.8}
              zone={[7.5, 8.3]}
              ticks={61}
              tint="sleep"
              labels={SCALES.sleep}
            />
            <Ruler
              min={0}
              max={21}
              value={12.4}
              zone={[10, 14]}
              ticks={64}
              tint="strain"
              labels={SCALES.strain}
            />
          </div>
          <OrbitGauge
            readiness={84}
            recovery={82}
            sleep={94}
            strain={12.4}
            label="Orbit gauge: readiness 84"
          />
        </Section>
      </Container>
    </PageShell>
  );
}

import tokens from '@/styles/tokens.json';
import { Logo, Lockup, Wordmark } from '@/components/Brand';
import { Mark } from '@/components/Mark';
import { PageShell } from '@/components/chrome';
import { ButtonLink, Card, Container, Label, SampleNote, Section, Tile, Todo } from '@/components/ui';
import '@/styles/swatches.css';
import styles from './Styleguide.module.css';

const typeSamples: [keyof typeof tokens.type.scale, string][] = [
  ['display', '84'],
  ['number-large', '7h 48m'],
  ['title', 'Good morning'],
  ['metric', '82 %'],
  ['headline', 'HRV above your baseline'],
  ['body', 'Your HRV is 12 % above your 30-day average.'],
  ['callout', 'Quality is 60 % performance, 20 % efficiency, 20 % restorative.'],
  ['caption', 'Model 2026.10.1'],
  ['label', 'Overall readiness'],
  ['axis', 'M T W T F S S'],
];

export function StyleguidePage() {
  const colors = Object.entries(tokens.colors);
  return (
    <PageShell locale="en" path="/styleguide" translated={false}>
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
            <Mark size={18} />
            <span className={styles.accent}>
              <Mark size={48} />
            </span>
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
                <span className={styles.swatchValue}>
                  dark {value.dark}
                  {value.light !== value.dark && <> · light {value.light}</>}
                </span>
              </li>
            ))}
          </ul>
        </Section>

        <Section
          id="type"
          title="Type"
          intro={<p>Satoshi, the app&apos;s scale in rem. Headings clamp between two steps.</p>}
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

        <Section id="components" title="Components">
          <div className={styles.row}>
            <ButtonLink href="#components">Join the beta</ButtonLink>
            <ButtonLink href="#components" variant="secondary">
              How it works
            </ButtonLink>
            <ButtonLink href="#components" variant="quiet">
              Quiet link
            </ButtonLink>
          </div>
          <div className={styles.row}>
            <Label>Secondary</Label>
            <Label tone="accent">Insight</Label>
            <Label tone="warning">Notice</Label>
            <Label tone="alert">Attention</Label>
          </div>
          <div className={styles.grid}>
            <Card>
              <Label>Card</Label>
              <p className={styles.cardTitle}>Radius 18, padding 18</p>
              <p className={styles.muted}>The app&apos;s card on the surface colour.</p>
            </Card>
            <Tile>
              <Label>Tile</Label>
              <p className={styles.metric}>82 %</p>
            </Tile>
            <Card>
              <p>
                A draft value: <Todo>TODO(user): support email address</Todo>
              </p>
              <SampleNote>Sample data</SampleNote>
            </Card>
          </div>
        </Section>
      </Container>
    </PageShell>
  );
}

// An insight card (InsightCard.swift): a `surface` card, a 6 px severity dot and the severity's word, the
// title, the body. No coloured stripe and no kind glyph: the colour is on the dot only (amber for a
// notice, coral for "Attention", a hollow ring for an insight), and the dot's shape and the word say it
// too. `glow` is the Insights tile: the same rows in the recovery glow, with the Volt hint dot.
import { GlowTile } from './instruments';
import styles from './insight.module.css';

export type Severity = 'info' | 'notice' | 'warn';

export function InsightCard({
  severity,
  word,
  title,
  body,
  glow,
}: {
  severity: Severity;
  word: string;
  title: string;
  body: string;
  glow?: boolean;
}) {
  const rows = (
    <>
      <p className={styles.word}>
        <span className={`${styles.dot} ${styles[`dot-${glow ? 'hint' : severity}`]}`} aria-hidden="true" />
        {word}
      </p>
      <h3 className={glow ? styles.glowTitle : styles.title}>{title}</h3>
      <p className={glow ? styles.glowBody : styles.body}>{body}</p>
    </>
  );
  if (glow) {
    return (
      <GlowTile metric="recovery" as="article" hero className={styles.card}>
        {rows}
      </GlowTile>
    );
  }
  return <article className={`${styles.card} ${styles.plain}`}>{rows}</article>;
}

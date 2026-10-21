// Instrument-style figures drawn in SVG, after ../app/docs/CHART_STYLE.md: 1.5 px linear
// lines, a trailing y axis, score scales fixed with their band thresholds as dashed rules,
// markers only at today (filled) and the window's min and max (hollow), no fill, no glow.
// Colours are token classes; numbers are tabular. Every figure states it is sample data.
import { Mark } from './Mark';
import styles from './charts.module.css';

/** The readiness ring: a track and an arc from 12 o'clock, clockwise. */
export function Gauge({
  value,
  max = 100,
  label,
  valueText,
  size = 220,
}: {
  value: number;
  max?: number;
  label: string;
  valueText?: string;
  size?: number;
}) {
  const r = 46;
  const c = 2 * Math.PI * r;
  const filled = Math.max(0, Math.min(1, value / max)) * c;
  return (
    <div className={styles.gauge}>
      <svg viewBox="0 0 100 100" width={size} height={size} aria-hidden="true" focusable="false">
        <circle className={styles.track} cx="50" cy="50" r={r} strokeWidth="4.5" fill="none" />
        <circle
          className={styles.arc}
          cx="50"
          cy="50"
          r={r}
          strokeWidth="4.5"
          fill="none"
          strokeLinecap="round"
          strokeDasharray={`${filled.toFixed(2)} ${c.toFixed(2)}`}
          transform="rotate(-90 50 50)"
        />
      </svg>
      <div className={styles.gaugeCentre}>
        <Mark size={26} className={styles.gaugeMark} />
        <span className={styles.gaugeValue}>{valueText ?? value}</span>
        <span className={styles.gaugeLabel}>{label}</span>
      </div>
    </div>
  );
}

type TrendProps = {
  values: number[];
  /** One short label per value (weekday initials). */
  ticks: string[];
  /** Fixed scale and band thresholds (CHART_STYLE: scores keep 0–100 with 33/67). */
  min?: number;
  max?: number;
  thresholds?: number[];
  /** The accessible summary: what it shows, and that it is sample data. */
  label: string;
  format?: (v: number) => string;
};

/** A daily series with a trailing axis, thresholds, and today annotated. */
export function TrendChart({
  values,
  ticks,
  min = 0,
  max = 100,
  thresholds = [33, 67],
  label,
  format = String,
}: TrendProps) {
  const W = 320;
  const H = 132;
  const pad = { l: 8, r: 34, t: 14, b: 22 };
  const x = (i: number) => pad.l + (i * (W - pad.l - pad.r)) / Math.max(1, values.length - 1);
  const y = (v: number) => pad.t + ((max - v) * (H - pad.t - pad.b)) / (max - min);
  const path = values.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(v).toFixed(1)}`).join('');
  const last = values.length - 1;
  const lo = values.indexOf(Math.min(...values));
  const hi = values.indexOf(Math.max(...values));
  const axis = [min, ...thresholds, max];
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className={styles.trend} role="img" aria-label={label}>
      {axis.map((t) => (
        <g key={t}>
          <line
            x1={pad.l}
            x2={W - pad.r}
            y1={y(t)}
            y2={y(t)}
            className={thresholds.includes(t) ? styles.threshold : styles.grid}
            strokeWidth={thresholds.includes(t) ? 1 : 0.5}
            strokeDasharray={thresholds.includes(t) ? '3 3' : undefined}
          />
          <text x={W - pad.r + 6} y={y(t) + 3.5} className={styles.axis}>
            {t}
          </text>
        </g>
      ))}
      <path d={path} className={styles.line} fill="none" strokeWidth="1.5" strokeLinejoin="round" />
      {[lo, hi]
        .filter((i) => i !== last)
        .map((i) => (
          <circle key={i} cx={x(i)} cy={y(values[i])} r="3" className={styles.hollow} strokeWidth="1.5" />
        ))}
      <circle cx={x(last)} cy={y(values[last])} r="3.5" className={styles.today} />
      <text x={x(last) - 6} y={y(values[last]) - 8} textAnchor="end" className={styles.annotation}>
        {format(values[last])}
      </text>
      {ticks.map((t, i) => (
        <text key={i} x={x(i)} y={H - 6} textAnchor="middle" className={styles.axis}>
          {t}
        </text>
      ))}
    </svg>
  );
}

/** Small glyphs from the app's tiles: moon (sleep), heart (recovery), bolt (strain). */
export function MetricIcon({ kind }: { kind: 'sleep' | 'recovery' | 'strain' }) {
  const paths = {
    sleep: 'M15.5 3.5a8.5 8.5 0 1 0 5 15.4A9 9 0 0 1 15.5 3.5Z',
    recovery: 'M12 20.2 4.6 12.9a4.6 4.6 0 0 1 6.5-6.5l.9.9.9-.9a4.6 4.6 0 0 1 6.5 6.5L12 20.2Z',
    strain: 'M13.2 2.5 5.5 13.4h5.6l-1.3 8.1 7.7-10.9h-5.6l1.3-8.1Z',
  } as const;
  return (
    <svg
      viewBox="0 0 24 24"
      width="22"
      height="22"
      aria-hidden="true"
      focusable="false"
      className={styles[`icon-${kind}`]}
    >
      <path
        d={paths[kind]}
        fill={kind === 'recovery' ? 'none' : 'currentColor'}
        stroke="currentColor"
        strokeWidth={kind === 'recovery' ? 1.8 : 0}
        strokeLinejoin="round"
      />
    </svg>
  );
}

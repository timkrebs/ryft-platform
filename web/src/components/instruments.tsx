// Nocturne's instruments, drawn as static SVG and CSS (no script): the dot-grid numeral, the ruler, the
// day dots, the glow tile, the orbit gauge v3 and a sparkline. Geometry is the app's (DotNumeral.swift,
// RulerScale.swift, DotRow.swift, GlowTile.swift, OrbitGauge.swift); web/docs/NOCTURNE.md lists each
// number with its source. Every figure on the site made with them is sample data and says so.
import type { ReactNode } from 'react';
import { DOT_HEIGHT, dotPaths } from '@/lib/dots';
import styles from './instruments.module.css';

const cx = (...names: (string | false | undefined)[]) => names.filter(Boolean).join(' ');
const n2 = (v: number) => +v.toFixed(2);

// --- DotNumeral ---------------------------------------------------------------------------------

/**
 * The hero number as a grid of dots (5 × 7 cells per digit, dots 5 apart 7). `label` is what a screen
 * reader says: the grid is a drawing, not text.
 */
export function DotNumeral({
  text,
  label,
  glyphGap = 8,
  className,
}: {
  text: string;
  label: string;
  glyphGap?: number;
  className?: string;
}) {
  const { lit, off, width } = dotPaths(text, glyphGap);
  return (
    <svg
      viewBox={`0 0 ${width} ${DOT_HEIGHT}`}
      width={width}
      height={DOT_HEIGHT}
      className={cx(styles.numeral, className)}
      role="img"
      aria-label={label}
    >
      <path d={off} className={styles.dotOff} />
      <path d={lit} className={styles.dotOn} />
    </svg>
  );
}

// --- Ruler --------------------------------------------------------------------------------------

type RulerProps = {
  min: number;
  max: number;
  value: number;
  /** The band the day should be in: its ticks are longer and tinted. */
  zone?: [number, number];
  /** RulerScale.Ticks: 61 for Today and Sleep, 64 for Strain, 43 for the compact Strain ruler. */
  ticks: number;
  /** Edge to edge under the ruler: "0 · low · moderate · high · 100". */
  labels?: string[];
  tint?: 'ink' | 'sleep' | 'strain';
  /** Inside a glow tile: the labels take the tile's label ink, and the ruler is the compact one. */
  compact?: boolean;
  className?: string;
};

/**
 * RulerScale.swift: ticks of 1 × 8 in ink at 22 %, the zone's of 1.5 × 14 tinted, a Volt needle (a
 * triangle 12 × 8 and a line 1.5 × 20) at the value. Tick i is centred at i / (n − 1) of the width and
 * stands for min + i × (max − min) / (n − 1). Decorative here: the figure's text says the value.
 */
export function Ruler({
  min,
  max,
  value,
  zone,
  ticks,
  labels = [],
  tint = 'ink',
  compact,
  className,
}: RulerProps) {
  const lo = compact ? 7 : 8;
  const outside: number[] = [];
  const inside: number[] = [];
  for (let i = 0; i < ticks; i++) {
    const v = min + (i * (max - min)) / (ticks - 1);
    const pos = n2((i / (ticks - 1)) * 100);
    if (zone && v >= zone[0] - 1e-9 && v <= zone[1] + 1e-9) inside.push(pos);
    else outside.push(pos);
  }
  const needle = n2(Math.max(0, Math.min(1, (value - min) / (max - min))) * 100);
  const mid = compact ? 17 : 19;
  const height = compact ? 32 : 36;
  const zoneHalf = compact ? 6 : 7;
  return (
    <div className={cx(styles.ruler, compact && styles.rulerCompact, className)} aria-hidden="true">
      <svg width="100%" height={height} className={styles.rulerSvg} focusable="false">
        <g className={styles.tickOff}>
          {outside.map((p) => (
            <line key={p} x1={`${p}%`} x2={`${p}%`} y1={mid - lo / 2} y2={mid + lo / 2} />
          ))}
        </g>
        <g className={cx(styles.tickZone, styles[`zone-${tint}`])}>
          {inside.map((p) => (
            <line key={p} x1={`${p}%`} x2={`${p}%`} y1={mid - zoneHalf} y2={mid + zoneHalf} />
          ))}
        </g>
        <svg x={`${needle}%`} y="0" overflow="visible" className={styles.needle}>
          <polygon points="-6,0 6,0 0,8" />
          <line x1="0" x2="0" y1="8" y2={compact ? 24 : 28} />
        </svg>
      </svg>
      {labels.length > 0 && (
        <div className={styles.rulerLabels}>
          {labels.map((l) => (
            <span key={l}>{l}</span>
          ))}
        </div>
      )}
    </div>
  );
}

// --- DotRow -------------------------------------------------------------------------------------

/** DotRow.Mark: full, missed, no data differ by shape; `today` is the Volt marker, put last. */
export type DotMark = 'full' | 'missed' | 'today' | 'noData';

/** Day dots: 6 px, 4 apart (DotRow.swift). Decorative; the caller's text carries the meaning. */
export function DotRow({ marks, className }: { marks: DotMark[]; className?: string }) {
  return (
    <span className={cx(styles.dotRow, className)} aria-hidden="true">
      {marks.map((m, i) => (
        <span key={i} className={cx(styles.day, styles[`day-${m}`])} />
      ))}
    </span>
  );
}

// --- GlowTile -----------------------------------------------------------------------------------

/** One glow tile per section: the metric's colour under a dark radial core; text sits in the core. */
export function GlowTile({
  metric,
  children,
  as: Tag = 'div',
  hero,
  className,
}: {
  metric: 'recovery' | 'sleep' | 'strain';
  children: ReactNode;
  as?: 'div' | 'article' | 'li' | 'figure';
  /** Radius 28 (the Sleep and Strain hero tiles) instead of 24 (Today's tiles). */
  hero?: boolean;
  className?: string;
}) {
  return (
    <Tag className={cx(styles.glow, styles[`glow-${metric}`], hero && styles.glowHero, className)}>
      {children}
    </Tag>
  );
}

/** A label inside a glow tile (ryftGlowLabelStyle): ink at 78 %, full in the recovery tile. */
export function GlowLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={cx(styles.glowLabel, className)}>{children}</p>;
}

// --- OrbitGauge v3 ------------------------------------------------------------------------------

type GaugeProps = {
  /** 0–100: the inner ring, its lit ticks and the Volt dot. */
  readiness: number;
  /** 0–100 and 0–100: the middle ring's two halves, recovery left and sleep right. */
  recovery: number;
  sleep: number;
  /** 0–21: the outer ring. */
  strain: number;
  label: string;
  className?: string;
};

const S = 260;
const C = S / 2;
const polar = (r: number, deg: number) => {
  const a = (deg * Math.PI) / 180;
  return [C + r * Math.cos(a), C + r * Math.sin(a)] as const;
};
/** Angular distance in degrees, 0…180. */
const gap = (a: number, b: number) => Math.abs(((((a - b) % 360) + 540) % 360) - 180);

function arc(r: number, from: number, to: number) {
  const [x0, y0] = polar(r, from);
  const [x1, y1] = polar(r, to);
  const large = Math.abs(to - from) > 180 ? 1 : 0;
  const sweep = to > from ? 1 : 0;
  return `M${n2(x0)} ${n2(y0)}A${r} ${r} 0 ${large} ${sweep} ${n2(x1)} ${n2(y1)}`;
}

/** Ticks centred on radius r, `length` long, at the angles that are not within `cut` degrees of a dot. */
function tickRing(
  r: number,
  count: number,
  length: number,
  indices: number[],
  cutAt: number | null,
  cut: number,
) {
  let d = '';
  for (const i of indices) {
    const deg = -90 + (i * 360) / count;
    if (cutAt !== null && gap(deg, cutAt) < cut) continue;
    const [x0, y0] = polar(r - length / 2, deg);
    const [x1, y1] = polar(r + length / 2, deg);
    d += `M${n2(x0)} ${n2(y0)}L${n2(x1)} ${n2(y1)}`;
  }
  return d;
}

/**
 * OrbitGauge v3 at 260 (the Today board): strain outside as 128 ticks under a 3.5 arc from the top,
 * recovery (counter-clockwise) and sleep (clockwise) on the middle ring, split at the vertical with
 * 6° either side, readiness inside as 60 ticks with a Volt dot at the value. The three orbit dots
 * carry a 3 px ring of the background and a halo at 16 %. The centre is the dot numeral.
 */
export function OrbitGauge({ readiness, recovery, sleep, strain, label, className }: GaugeProps) {
  const strainHead = -90 + Math.min(strain / 21, 0.99999) * 360;
  const recoveryHead = -96 - (recovery / 100) * 160;
  const sleepHead = -84 + (sleep / 100) * 160;
  const voltAt = -90 + Math.max(0, Math.min(100, readiness)) * 3.6;
  const lit = Math.max(0, Math.min(60, Math.round((readiness / 100) * 60)));
  const all = (count: number) => Array.from({ length: count }, (_, i) => i);
  // Dot cut: the strain dot's 10 px at r 118 is 4.857°, the Volt dot's 7.5 px at r 58 is 7.41°.
  const outerTicks = tickRing(118, 128, 6, all(128), strainHead, 4.857);
  const litTicks = tickRing(58, 60, 7, all(lit), voltAt, 7.41);
  const unlitTicks = tickRing(58, 60, 7, all(60).slice(lit), voltAt, 7.41);
  const heads: [number, number, 'strain' | 'recovery' | 'sleep'][] = [
    [118, strainHead, 'strain'],
    [88, recoveryHead, 'recovery'],
    [88, sleepHead, 'sleep'],
  ];
  const numeral = dotPaths(String(Math.round(readiness)), 8, C - 37, C - DOT_HEIGHT / 2);
  const [vx, vy] = polar(58, voltAt);
  return (
    <svg
      viewBox="-4 -4 268 268"
      className={cx(styles.gauge, className)}
      role="img"
      aria-label={label}
      focusable="false"
    >
      <path d={outerTicks} className={styles.gaugeOuter} />
      <circle cx={C} cy={C} r="88" className={styles.gaugeMiddle} />
      <path d={unlitTicks} className={styles.gaugeInner} />
      <path d={litTicks} className={styles.gaugeLit} />
      <path d={arc(118, -90, strainHead)} className={styles.arcStrain} />
      <path d={arc(88, -96, recoveryHead)} className={styles.arcRecovery} />
      <path d={arc(88, -84, sleepHead)} className={styles.arcSleep} />
      {heads.map(([r, deg, metric]) => {
        const [x, y] = polar(r, deg);
        return (
          <g key={metric} className={styles[`head-${metric}`]}>
            <circle cx={n2(x)} cy={n2(y)} r="15" className={styles.halo} />
            <circle cx={n2(x)} cy={n2(y)} r="8.5" className={styles.cut} />
            <circle cx={n2(x)} cy={n2(y)} r="7" className={styles.headDot} />
          </g>
        );
      })}
      <circle cx={n2(vx)} cy={n2(vy)} r="6.25" className={styles.voltCut} />
      <circle cx={n2(vx)} cy={n2(vy)} r="5" className={styles.volt} />
      <path d={numeral.off} className={styles.dotOff} />
      <path d={numeral.lit} className={styles.dotOn} />
    </svg>
  );
}

// --- Sparkline ----------------------------------------------------------------------------------

/** A 1.5 px line with its last day marked, for the recovery tile's HRV wave. Decorative. */
export function Sparkline({ values, className }: { values: number[]; className?: string }) {
  const W = 72;
  const H = 24;
  const lo = Math.min(...values);
  const hi = Math.max(...values);
  const pts = values.map((v, i) => [
    3 + (i * (W - 6)) / (values.length - 1),
    3 + ((hi - v) / (hi - lo)) * (H - 6),
  ]);
  const last = pts[pts.length - 1];
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      width={W}
      height={H}
      className={cx(styles.sparkline, className)}
      aria-hidden="true"
      focusable="false"
    >
      <polyline points={pts.map(([x, y]) => `${n2(x)},${n2(y)}`).join(' ')} />
      <circle cx={n2(last[0])} cy={n2(last[1])} r="2.5" />
    </svg>
  );
}

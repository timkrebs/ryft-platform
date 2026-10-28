// The RYFT orbit mark (2026-10-28), ported from ../../app/Ryft/Design/RyftMark.swift with
// the same numbers: three concentric round-capped rings, each broken where a dot sits on
// it. Outer ring: one dot (65°). Middle ring: two dots (-76°, 180°). Inner ring: closed,
// empty. Angles in degrees, 0 = 3 o'clock, clockwise (y points down), in a 100 × 100 box
// with the centre at 50/50; radii run to the stroke's centreline. The vector master is
// ../../app/design/ryft-mark.svg and tests/mark.test.ts compares against it.
//
// Optical size (the app's rule): the stroke never gets thinner than 1.25 px, so below
// 39.6 px it grows to 1.25 px; dots and clear air grow by the square root of that factor,
// and the inner ring is dropped below 22 px. Used by Mark.tsx and by the icon and OG-image
// scripts, so every rendering shares one geometry.

export const BOX = 100;
const CENTRE = 50;
const STROKE = 3.16;
const DOT_R = 6;
const AIR_FACTOR = 1;
const MIN_STROKE_PX = 1.25;
const INNER_RING_MIN_SIDE = 22;

type Ring = { radius: number; dots: number[] };
const OUTER: Ring = { radius: 44, dots: [65] };
const MIDDLE: Ring = { radius: 25.6, dots: [-76, 180] };
const INNER_R = 7.6;

/** The outer ring's span in the box (edge to edge); the app icon scales by it. */
export const OUTER_SPAN = 2 * OUTER.radius + STROKE;

export type MarkGeometry = {
  size: number;
  stroke: number;
  /** The visible ring arcs as SVG path data (stroked, round caps). */
  arcs: string[];
  /** Filled circles: the orbiting dots. */
  dots: { cx: number; cy: number; r: number }[];
  /** The inner ring (stroked), or null without it. */
  inner: { cx: number; cy: number; r: number } | null;
};

const round = (n: number) => Math.round(n * 1000) / 1000;
const rad = (deg: number) => (deg * Math.PI) / 180;
const point = (deg: number, r: number) => ({
  x: CENTRE + r * Math.cos(rad(deg)),
  y: CENTRE + r * Math.sin(rad(deg)),
});

/** Stroke, dot radius and inner-ring visibility for a rendered side in px. */
export function markMetrics(side: number) {
  if (!(side > 0)) return { stroke: STROKE, dotRadius: DOT_R, showsInnerRing: false };
  const k = Math.max(1, MIN_STROKE_PX / ((STROKE * side) / BOX));
  return {
    stroke: STROKE * k,
    dotRadius: DOT_R * Math.sqrt(k),
    showsInnerRing: side >= INNER_RING_MIN_SIDE,
  };
}

/**
 * The geometry for a mark rendered `side` px wide (its 100-box, not the ring). The default
 * is the nominal 100: no small-size correction. `showsInnerRing` overrides the size rule.
 */
export function markGeometry(side = BOX, showsInnerRing?: boolean): MarkGeometry {
  const m = markMetrics(side);
  const air = AIR_FACTOR * Math.sqrt(STROKE * m.stroke);
  const halfGap = (r: number) => {
    const chord = m.dotRadius + m.stroke / 2 + air;
    return ((2 * Math.asin(Math.min(1, chord / (2 * r)))) * 180) / Math.PI;
  };

  const arcs: string[] = [];
  const dots: MarkGeometry['dots'] = [];
  for (const ring of [OUTER, MIDDLE]) {
    const gap = halfGap(ring.radius);
    const angles = [...ring.dots].sort((a, b) => a - b);
    angles.forEach((a, i) => {
      const next = i === angles.length - 1 ? angles[0] + 360 : angles[i + 1];
      const start = a + gap;
      const end = next - gap;
      // Neighbouring gaps that meet leave no arc between the dots.
      if (end > start) {
        const from = point(start, ring.radius);
        const to = point(end, ring.radius);
        const large = end - start > 180 ? 1 : 0;
        arcs.push(
          `M${round(from.x)} ${round(from.y)}A${ring.radius} ${ring.radius} 0 ${large} 1 ${round(to.x)} ${round(to.y)}`,
        );
      }
      const p = point(a, ring.radius);
      dots.push({ cx: round(p.x), cy: round(p.y), r: round(m.dotRadius) });
    });
  }

  const inner = showsInnerRing ?? m.showsInnerRing;
  return {
    size: BOX,
    stroke: round(m.stroke),
    arcs,
    dots,
    inner: inner ? { cx: CENTRE, cy: CENTRE, r: INNER_R } : null,
  };
}

/**
 * The mark as a standalone SVG string. `size` is the rendered px width of the whole image;
 * `padding` is in box units and may be negative (the app icon's ring sits at 0.963 of the
 * tile, so the box is cropped). The small-size rule sees the mark's own frame.
 */
export function markSvg({
  color = 'currentColor',
  background,
  size = 100,
  padding = 0,
  showsInnerRing,
}: {
  color?: string;
  background?: string;
  size?: number;
  padding?: number;
  showsInnerRing?: boolean;
} = {}): string {
  const box = BOX + padding * 2;
  const g = markGeometry((size * BOX) / box, showsInnerRing);
  const parts = [
    `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="${-padding} ${-padding} ${box} ${box}">`,
    background
      ? `<rect x="${-padding}" y="${-padding}" width="${box}" height="${box}" fill="${background}"/>`
      : '',
    `<g fill="none" stroke="${color}" stroke-width="${g.stroke}" stroke-linecap="round">`,
    ...g.arcs.map((d) => `<path d="${d}"/>`),
    g.inner ? `<circle cx="${g.inner.cx}" cy="${g.inner.cy}" r="${g.inner.r}"/>` : '',
    `</g>`,
    `<g fill="${color}">`,
    ...g.dots.map((d) => `<circle cx="${d.cx}" cy="${d.cy}" r="${d.r}"/>`),
    `</g>`,
    `</svg>`,
  ];
  return parts.join('');
}

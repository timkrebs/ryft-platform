// The RYFT orbit mark, ported from ../app/Ryft/Design/RyftMark.swift with the same
// geometry: a thin ring broken where two small dots sit on it (upper right, lower
// left), a small inner orbit and a solid centre dot. Angles in degrees, 0 = 3 o'clock,
// clockwise (y points down), in a 100 × 100 box. Used by Mark.tsx and by the icon and
// OG-image scripts, so every rendering shares one geometry.

export type MarkGeometry = {
  size: number;
  stroke: number;
  /** The outer ring's two arcs as SVG path data (stroked, round caps). */
  arcs: string[];
  /** Filled circles: the two orbiting dots and the centre dot. */
  dots: { cx: number; cy: number; r: number }[];
  /** The inner orbit (stroked), or null without it. */
  inner: { cx: number; cy: number; r: number } | null;
};

const round = (n: number) => Math.round(n * 1000) / 1000;

export function markGeometry(showsInnerOrbit = true): MarkGeometry {
  const side = 100;
  const c = side / 2;
  const stroke = side * 0.042;
  const ringR = side * 0.43;
  const dotR = side * 0.064;
  const centreR = side * 0.07;
  const innerR = side * 0.17;
  const dotAngles = [-52, 128];
  // A little air between ring and dot so the dot reads as orbiting.
  const gap = (Math.asin(Math.min(1, (dotR + stroke * 1.1) / ringR)) * 180) / Math.PI;
  const point = (deg: number, r: number) => {
    const rad = (deg * Math.PI) / 180;
    return { x: round(c + r * Math.cos(rad)), y: round(c + r * Math.sin(rad)) };
  };

  const arcs = dotAngles.map((a, i) => {
    const next = dotAngles[(i + 1) % dotAngles.length] + (i === dotAngles.length - 1 ? 360 : 0);
    const from = point(a + gap, ringR);
    const to = point(next - gap, ringR);
    const large = next - a - 2 * gap > 180 ? 1 : 0;
    return `M${from.x} ${from.y}A${ringR} ${ringR} 0 ${large} 1 ${to.x} ${to.y}`;
  });
  const dots = dotAngles.map((a) => {
    const p = point(a, ringR);
    return { cx: p.x, cy: p.y, r: round(dotR) };
  });
  dots.push({ cx: c, cy: c, r: round(centreR) });

  return {
    size: side,
    stroke: round(stroke),
    arcs,
    dots,
    inner: showsInnerOrbit ? { cx: c, cy: c, r: round(innerR) } : null,
  };
}

/** The mark as a standalone SVG string, for icons and images. */
export function markSvg({
  color = 'currentColor',
  background,
  size = 100,
  padding = 0,
  showsInnerOrbit = true,
}: {
  color?: string;
  background?: string;
  size?: number;
  padding?: number;
  showsInnerOrbit?: boolean;
} = {}): string {
  const g = markGeometry(showsInnerOrbit);
  const box = g.size + padding * 2;
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

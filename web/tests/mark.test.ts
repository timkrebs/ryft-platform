import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { markGeometry, markMetrics, markSvg, OUTER_SPAN } from '../src/lib/mark';

const angle = (d: { cx: number; cy: number }) => (Math.atan2(d.cy - 50, d.cx - 50) * 180) / Math.PI;
const dist = (d: { cx: number; cy: number }) => Math.hypot(d.cx - 50, d.cy - 50);

// The port of RyftMark.swift (2026-10-28): the same numbers in a 100 × 100 box.
describe('the orbit mark', () => {
  const g = markGeometry();

  it('keeps the app’s proportions', () => {
    expect(g.stroke).toBeCloseTo(3.16);
    expect(g.inner).toEqual({ cx: 50, cy: 50, r: 7.6 });
    expect(g.dots.map((d) => d.r)).toEqual([6, 6, 6]);
    expect(OUTER_SPAN).toBeCloseTo(91.16);
  });

  it('puts one dot on the outer ring and two on the middle ring', () => {
    expect(g.dots).toHaveLength(3);
    const [outer, ...middle] = g.dots;
    expect(angle(outer)).toBeCloseTo(65, 2);
    expect(dist(outer)).toBeCloseTo(44, 2);
    expect(middle.map((d) => Math.round(angle(d)))).toEqual([-76, 180]);
    for (const d of middle) expect(dist(d)).toBeCloseTo(25.6, 2);
    // No centre dot: every dot sits on a ring.
    expect(g.dots.some((d) => dist(d) < 1)).toBe(false);
  });

  it('breaks the rings into one outer and two middle arcs', () => {
    expect(g.arcs).toHaveLength(3);
  });

  it('leaves 10.74 between each ring end and the dot centre', () => {
    // Ends of the outer arc: start/end points are the first and last coordinate pairs.
    const [outerArc] = g.arcs;
    const n = outerArc.match(/-?\d+(?:\.\d+)?/g)!.map(Number);
    const dot = g.dots[0];
    for (const [x, y] of [
      [n[0], n[1]],
      [n[n.length - 2], n[n.length - 1]],
    ])
      expect(Math.hypot(x - dot.cx, y - dot.cy)).toBeCloseTo(10.74, 1);
  });

  it('keeps everything inside the box; the 65° dot touches 50 from the centre', () => {
    for (const d of g.dots) {
      expect(d.cx - d.r).toBeGreaterThanOrEqual(0);
      expect(d.cy + d.r).toBeLessThanOrEqual(100);
    }
    expect(dist(g.dots[0]) + g.dots[0].r).toBeCloseTo(50, 2);
  });

  it('drops the inner ring on request', () => {
    expect(markGeometry(100, false).inner).toBeNull();
    expect(markSvg({ showsInnerRing: false }).match(/<circle/g)).toHaveLength(3);
    expect(markSvg().match(/<circle/g)).toHaveLength(4);
  });
});

describe('the small-size rule', () => {
  it('is nominal from 39.6 px up', () => {
    expect(markMetrics(39.6).stroke).toBeCloseTo(3.16, 2);
    expect(markMetrics(44)).toEqual({ stroke: 3.16, dotRadius: 6, showsInnerRing: true });
  });

  it('thickens the stroke to 1.25 px and grows dots by √k', () => {
    for (const side of [18, 24, 32]) {
      const m = markMetrics(side);
      expect((m.stroke * side) / 100).toBeCloseTo(1.25, 3);
      expect(m.dotRadius).toBeCloseTo(6 * Math.sqrt(m.stroke / 3.16), 3);
    }
    expect(markMetrics(18).dotRadius).toBeCloseTo(8.89, 2);
    expect(markMetrics(24).dotRadius).toBeCloseTo(7.7, 2);
  });

  it('drops the inner ring below 22 px and keeps every arc', () => {
    expect(markMetrics(21.9).showsInnerRing).toBe(false);
    expect(markMetrics(22).showsInnerRing).toBe(true);
    expect(markGeometry(18).inner).toBeNull();
    expect(markGeometry(18).arcs).toHaveLength(3);
  });
});

// Drift check against the app's vector master. Like tokens:check it needs the app next
// to the platform: skipped without it, an error in CI. Green once the app has the new mark.
const APP = resolve(import.meta.dirname, '..', process.env.RYFT_APP_DIR ?? '../../app');
const master = resolve(APP, 'design/ryft-mark.svg');
describe('drift against the app’s design/ryft-mark.svg', () => {
  const nums = (s: string) => s.match(/-?\d+(?:\.\d+)?/g)!.map(Number);
  const present = existsSync(master);
  if (!present && process.env.CI) {
    it('finds the app’s mark', () => {
      throw new Error(`${master} not found (set RYFT_APP_DIR).`);
    });
    return;
  }
  it.skipIf(!present)('has the same stroke, arcs, dots and inner ring', () => {
    const svg = readFileSync(master, 'utf8');
    const g = markGeometry();
    const appArcs = [...svg.matchAll(/<path d="([^"]+)"/g)].map((m) => nums(m[1]));
    const appDots = [...svg.matchAll(/<circle cx="([\d.]+)" cy="([\d.]+)" r="6"/g)].map((m) =>
      nums(`${m[1]} ${m[2]}`),
    );
    expect(Number(/stroke-width="([\d.]+)"/.exec(svg)![1])).toBeCloseTo(g.stroke, 3);
    expect(appArcs).toHaveLength(g.arcs.length);
    appArcs.forEach((a, i) => {
      const mine = nums(g.arcs[i]);
      expect(mine).toHaveLength(a.length);
      mine.forEach((v, j) => expect(v).toBeCloseTo(a[j], 2));
    });
    expect(appDots).toHaveLength(g.dots.length);
    appDots.forEach(([x, y], i) => {
      expect(g.dots[i].cx).toBeCloseTo(x, 2);
      expect(g.dots[i].cy).toBeCloseTo(y, 2);
    });
    expect(svg).toContain(`<circle cx="50" cy="50" r="${g.inner!.r}"/>`);
  });
});

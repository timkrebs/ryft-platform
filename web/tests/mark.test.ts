import { describe, expect, it } from 'vitest';
import { markGeometry, markSvg } from '../src/lib/mark';

// The port of RyftMark.swift: the same proportions in a 100 × 100 box.
describe('the orbit mark', () => {
  const g = markGeometry();

  it('keeps the app’s proportions', () => {
    expect(g.stroke).toBeCloseTo(4.2);
    expect(g.inner?.r).toBeCloseTo(17);
    expect(g.dots.map((d) => d.r)).toEqual([6.4, 6.4, 7]);
  });

  it('puts the dots on the ring at -52° and 128°', () => {
    const angle = (d: { cx: number; cy: number }) => (Math.atan2(d.cy - 50, d.cx - 50) * 180) / Math.PI;
    expect(angle(g.dots[0])).toBeCloseTo(-52, 2);
    expect(angle(g.dots[1])).toBeCloseTo(128, 2);
    for (const d of g.dots.slice(0, 2)) expect(Math.hypot(d.cx - 50, d.cy - 50)).toBeCloseTo(43, 2);
  });

  it('breaks the ring into two equal arcs that stay inside the box', () => {
    expect(g.arcs).toHaveLength(2);
    for (const d of g.dots) {
      expect(d.cx - d.r).toBeGreaterThanOrEqual(0);
      expect(d.cy + d.r).toBeLessThanOrEqual(100);
    }
    const ends = g.arcs.map((a) => a.match(/[\d.]+/g)!.map(Number));
    // Point symmetry around the centre: arc 2 is arc 1 rotated by 180°.
    expect(ends[0][0] + ends[1][0]).toBeCloseTo(100, 2);
    expect(ends[0][1] + ends[1][1]).toBeCloseTo(100, 2);
  });

  it('drops the inner orbit on request', () => {
    expect(markGeometry(false).inner).toBeNull();
    expect(markSvg({ showsInnerOrbit: false }).match(/<circle/g)).toHaveLength(3);
  });
});

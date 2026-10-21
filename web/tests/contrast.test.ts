import { describe, expect, it } from 'vitest';
import tokens from '../src/styles/tokens.json';

// WCAG 2.2 AA against the generated tokens, in both appearances: every text colour the
// site uses on every surface it sits on reaches 4.5:1; chart and gauge marks reach 3:1.
type Scheme = 'dark' | 'light';
type Color = keyof typeof tokens.colors;

function parse(value: string): [number, number, number, number] {
  const hex = value.match(/^#([0-9a-f]{6})$/i);
  if (hex) {
    const n = Number.parseInt(hex[1], 16);
    return [(n >> 16) & 255, (n >> 8) & 255, n & 255, 1];
  }
  const rgb = value.match(/^rgb\((\d+) (\d+) (\d+) \/ ([\d.]+)\)$/);
  if (rgb) return [+rgb[1], +rgb[2], +rgb[3], +rgb[4]];
  throw new Error(`unparsed colour ${value}`);
}

/** The colour as seen on an opaque background. */
function over(fg: string, bg: string): [number, number, number] {
  const [r, g, b, a] = parse(fg);
  const [R, G, B] = parse(bg);
  return [r * a + R * (1 - a), g * a + G * (1 - a), b * a + B * (1 - a)];
}

function luminance([r, g, b]: [number, number, number]) {
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

function contrast(fg: Color, bg: Color, scheme: Scheme): number {
  const back = over(tokens.colors[bg][scheme], tokens.colors.background[scheme]);
  const backHex = `#${back.map((c) => Math.round(c).toString(16).padStart(2, '0')).join('')}`;
  const front = over(tokens.colors[fg][scheme], backHex);
  const [l1, l2] = [luminance(front), luminance(back)].sort((x, y) => y - x);
  return (l1 + 0.05) / (l2 + 0.05);
}

const text: [Color, Color][] = [
  ['text-primary', 'background'],
  ['text-primary', 'surface'],
  ['text-primary', 'surface-raised'],
  ['text-secondary', 'background'],
  ['text-secondary', 'surface'],
  ['text-tertiary', 'background'],
  ['text-tertiary', 'surface'],
  ['accent', 'background'],
  ['accent', 'surface'],
  ['warning', 'background'],
  ['warning', 'surface'],
  ['alert', 'background'],
  ['alert', 'surface'],
  ['on-accent', 'accent'],
];

const marks: [Color, Color][] = [
  ['accent', 'surface'],
  ['metric-strain', 'surface'],
  ['metric-sleep', 'surface'],
  ['accent-secondary', 'surface'],
];

describe.each(['dark', 'light'] as const)('contrast in %s', (scheme) => {
  it.each(text)('%s on %s is at least 4.5:1', (fg, bg) => {
    expect(contrast(fg, bg, scheme)).toBeGreaterThanOrEqual(4.5);
  });

  it.each(marks)('%s marks on %s are at least 3:1', (fg, bg) => {
    expect(contrast(fg, bg, scheme)).toBeGreaterThanOrEqual(3);
  });
});

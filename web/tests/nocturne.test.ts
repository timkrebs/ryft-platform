import { existsSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { GLYPHS, dotPaths } from '../src/lib/dots';

// web/docs/NOCTURNE.md: the numbers the web takes from the app's components that tokens.css doesn't
// carry live in src/styles/nocturne.css, each named after its Swift constant. This reads the Swift at
// the pin (web/.app-ref) and fails when one moved. Like tokens:check it skips without the app outside
// CI and fails when CI is set.
const WEB = resolve(import.meta.dirname, '..');
const APP = resolve(WEB, process.env.RYFT_APP_DIR ?? '../../app');
const hasApp = existsSync(join(APP, 'Ryft/Design/Theme.swift'));
const CSS = readFileSync(join(WEB, 'src/styles/nocturne.css'), 'utf8');

const swift = (file: string) => readFileSync(join(APP, file), 'utf8');
const constant = (file: string, name: string) => {
  // `static let a = 1, b = 2` declares both; a name is the first after `static let ` or after a comma.
  const m = new RegExp(`(?:static let |, )${name}(?::[^=,]+)? = ([\\d.]+)`).exec(swift(file));
  if (!m) throw new Error(`${file}: static let ${name} not found`);
  return +m[1];
};
/** The percentage a custom property mixes ink in at: `--ink-ring: color-mix(… 16%, transparent)`. */
const mixed = (prop: string) => {
  const m = new RegExp(`${prop}:\\s*color-mix\\(in srgb, var\\(--text-primary\\) ([\\d.]+)%`).exec(CSS);
  if (!m) throw new Error(`nocturne.css: ${prop} not found`);
  return +m[1] / 100;
};

const required = !!process.env.CI;
describe.runIf(hasApp || required)("the components' numbers at the app pin", () => {
  it('has the app (CI checks it out at web/.app-ref)', () => {
    expect(hasApp).toBe(true);
  });

  it.each([
    ['--ink-ring', 'Ryft/Design/CircleButton.swift', 'borderOpacity'],
    ['--ink-outline', 'Ryft/Design/PillButton.swift', 'outlineOpacity'],
    ['--ink-underline', 'Ryft/Design/InlineLink.swift', 'underlineOpacity'],
    ['--ink-pressed', 'Ryft/Design/CircleButton.swift', 'pressedFillOpacity'],
    ['--ink-accessory', 'Ryft/Design/GroupedList.swift', 'accessoryOpacity'],
    ['--ink-dot-off', 'Ryft/Design/DotNumeral.swift', 'offOpacity'],
    ['--ink-tick-unlit', 'Ryft/Design/TickRow.swift', 'unlitOpacity'],
    ['--ink-dot-missed', 'Ryft/Design/DotRow.swift', 'outlineOpacity'],
    ['--ink-dot-nodata', 'Ryft/Design/DotRow.swift', 'noDataOpacity'],
    ['--glow-label', 'Ryft/Design/GlowTile.swift', 'labelOpacity'],
    ['--ink-gauge-outer', 'Shared/OrbitGaugePalette.swift', 'outerTrackOpacity'],
    ['--ink-gauge-middle', 'Shared/OrbitGaugePalette.swift', 'middleTrackOpacity'],
    ['--ink-gauge-inner', 'Shared/OrbitGaugePalette.swift', 'innerTrackOpacity'],
  ])('%s is %s → %s', (prop, file, name) => {
    expect(mixed(prop)).toBeCloseTo(constant(file, name), 5);
  });

  it('draws the halo and the flat border as the app does', () => {
    const halo = /--halo-opacity:\s*([\d.]+)/.exec(CSS)![1];
    expect(+halo).toBe(constant('Shared/OrbitGaugePalette.swift', 'haloOpacity'));
    const border = /--glow-flat-border:\s*(\d+)px/.exec(CSS)![1];
    expect(+border).toBe(constant('Ryft/Design/GlowTile.swift', 'flatBorder'));
  });

  it("has the ruler's tick opacity and the dot numeral's lit dots", () => {
    const ruler = swift('Ryft/Design/RulerScale.swift');
    expect(/outsideOpacity: Double \{ self == \.regular \? ([\d.]+)/.exec(ruler)![1]).toBe('0.22');
    expect(mixed('--ink-tick-off')).toBeCloseTo(0.22, 5);
    // The lit dots per digit (the app's design.md, P2-5): 0 is 16, 1 is 10, 2 to 4 are 14, 5 is 17, ...
    const lit = [16, 10, 14, 14, 14, 17, 15, 11, 17, 15];
    lit.forEach((n, digit) => {
      const count = (dotPaths(String(digit), 8).lit.match(/M/g) ?? []).length;
      expect(count, `digit ${digit}`).toBe(n);
    });
  });

  it("draws every glyph of the app's table, row for row", () => {
    const source = swift('Ryft/Design/DotNumeral.swift');
    const table: Record<string, { w: number; rows: number[] }> = {};
    for (const [, ch, w, rows] of source.matchAll(/"(.)": DotGlyph\(width: (\d), rows: \[([^\]]+)\]/g)) {
      table[ch] = { w: +w, rows: rows.split(',').map((r) => Number.parseInt(r.trim().replace('0b', ''), 2)) };
    }
    expect(Object.keys(table)).toHaveLength(13);
    expect(table).toEqual(GLYPHS);
  });
});

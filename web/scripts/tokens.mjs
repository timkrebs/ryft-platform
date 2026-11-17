#!/usr/bin/env node
// Design tokens from the app, the single source of truth:
//   ../../app/Ryft/Assets.xcassets/*.colorset   → colours (one universal value each since
//                                                  Nocturne; a "dark" entry, if a set has one, wins)
//   ../../app/Ryft/Design/Theme.swift           → Metrics (radii, padding, spacing, sizes), motion
//   ../../app/Ryft/Design/Typography.swift      → type scale (pt → rem), weights, and the label's
//                                                  tracking and case (ryftLabelStyle's own body)
//   ../../app/Ryft/Design/RyftHeader.swift      → the wordmark's tracking (Wordmark.tracking, em)
// Writes src/styles/tokens.css (custom properties; dark-only like the app, web D20, so a light
// block appears only if a colour set has a light value of its own), src/styles/tokens.json (the
// same values for scripts; every colour as {light, dark}, equal when the set has one value) and
// src/styles/swatches.css (one class per colour, for /styleguide without inline styles).
//
//   node scripts/tokens.mjs          regenerate
//   node scripts/tokens.mjs --check  exit 1 when the committed files differ (CI)
//
// RYFT_APP_DIR overrides ../../app. CI checks the app out at the commit in web/.app-ref (platform
// D234) and runs --check there, so regenerate from a checkout at that commit.
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const ROOT = resolve(import.meta.dirname, '..');
const APP = resolve(ROOT, process.env.RYFT_APP_DIR ?? '../../app');
const CSS_OUT = join(ROOT, 'src/styles/tokens.css');
const JSON_OUT = join(ROOT, 'src/styles/tokens.json');
const SWATCHES_OUT = join(ROOT, 'src/styles/swatches.css');
const check = process.argv.includes('--check');

if (!existsSync(join(APP, 'Ryft/Assets.xcassets'))) {
  const message = `tokens: ${APP}/Ryft/Assets.xcassets not found (set RYFT_APP_DIR).`;
  if (check && !process.env.CI) {
    console.warn(`${message} Skipping the drift check outside CI.`);
    process.exit(0);
  }
  console.error(message);
  process.exit(1);
}

const tokens = {
  source:
    'app/Ryft at the commit in web/.app-ref (Assets.xcassets, Design/Theme.swift, Design/Typography.swift, Design/RyftHeader.swift)',
  colors: readColors(join(APP, 'Ryft/Assets.xcassets')),
  metrics: readMetrics(readFileSync(join(APP, 'Ryft/Design/Theme.swift'), 'utf8')),
  motion: readMotion(readFileSync(join(APP, 'Ryft/Design/Theme.swift'), 'utf8')),
  type: readType(
    readFileSync(join(APP, 'Ryft/Design/Typography.swift'), 'utf8'),
    readFileSync(join(APP, 'Ryft/Design/RyftHeader.swift'), 'utf8'),
  ),
};

const css = renderCss(tokens);
const json = `${JSON.stringify(tokens, null, 2)}\n`;
const swatches = renderSwatches(tokens);
const outputs = [
  [CSS_OUT, css],
  [JSON_OUT, json],
  [SWATCHES_OUT, swatches],
];

if (check) {
  const stale = outputs.filter(
    ([path, content]) => !existsSync(path) || readFileSync(path, 'utf8') !== content,
  );
  if (stale.length) {
    console.error(
      `tokens: out of date with the app: ${stale.map(([p]) => p.replace(`${ROOT}/`, '')).join(', ')}.`,
    );
    console.error('Run `npm run tokens` and commit the result.');
    process.exit(1);
  }
  console.log(`tokens: up to date (${Object.keys(tokens.colors).length} colours).`);
} else {
  for (const [path, content] of outputs) writeFileSync(path, content);
  console.log(
    `tokens: wrote ${Object.keys(tokens.colors).length} colours to src/styles/{tokens.css,tokens.json,swatches.css}.`,
  );
}

// --- colours -------------------------------------------------------------------------

function readColors(catalog) {
  const colors = {};
  const sets = readdirSync(catalog, { withFileTypes: true })
    .filter((e) => e.isDirectory() && e.name.endsWith('.colorset'))
    .map((e) => e.name)
    .sort();
  for (const set of sets) {
    const name = kebab(set.replace(/\.colorset$/, ''));
    const { colors: entries } = JSON.parse(readFileSync(join(catalog, set, 'Contents.json'), 'utf8'));
    let light;
    let dark;
    for (const entry of entries) {
      const appearances = entry.appearances ?? [];
      // High-contrast variants have no web counterpart yet; the base values already meet 4.5:1.
      if (appearances.some((a) => a.appearance === 'contrast')) continue;
      const luminosity = appearances.find((a) => a.appearance === 'luminosity')?.value;
      const value = cssColor(entry.color, `${set}`);
      if (luminosity === 'dark') dark = value;
      else light = value; // the universal value ("any"; "light" if a set ever has one)
    }
    if (!light) throw new Error(`tokens: ${set} has no universal colour`);
    // One value (every set since Nocturne, ADR 0016 in the app): light and dark are the same.
    colors[name] = { light, dark: dark ?? light };
  }
  return colors;
}

function cssColor(color, where) {
  const c = color.components;
  const alpha = Number.parseFloat(c.alpha ?? '1');
  const space = color['color-space'];
  if (space === 'gray-gamma-22' || space === 'extended-gray') {
    const w = channel(c.white, where);
    return rgb(w, w, w, alpha);
  }
  const [r, g, b] = [c.red, c.green, c.blue].map((v) => channel(v, where));
  if (space === 'srgb' || space === 'extended-srgb') return rgb(r, g, b, alpha);
  if (space === 'display-p3') {
    const f = (v) => +(v / 255).toFixed(4);
    return `color(display-p3 ${f(r)} ${f(g)} ${f(b)}${alpha < 1 ? ` / ${alpha}` : ''})`;
  }
  throw new Error(`tokens: ${where} uses colour space ${space}, which tokens.mjs doesn't map yet`);
}

// Xcode writes components as "0x3F" (8-bit hex), "0.247" (float) or "63" (8-bit integer).
function channel(raw, where) {
  const s = String(raw).trim();
  if (/^0x[0-9a-f]+$/i.test(s)) return Number.parseInt(s, 16);
  if (s.includes('.')) return Math.round(Number.parseFloat(s) * 255);
  if (/^\d+$/.test(s)) return Number.parseInt(s, 10);
  throw new Error(`tokens: ${where} has a component "${s}" tokens.mjs can't read`);
}

function rgb(r, g, b, alpha) {
  if (alpha >= 1) return `#${[r, g, b].map((v) => v.toString(16).padStart(2, '0')).join('')}`;
  return `rgb(${r} ${g} ${b} / ${+alpha.toFixed(3)})`;
}

// --- shapes, motion, type -------------------------------------------------------------

function readMetrics(theme) {
  const block = between(theme, 'enum Metrics {', '}', 'Theme.swift → Metrics');
  const out = {};
  for (const [, name, value] of block.matchAll(/static let (\w+): CGFloat = ([\d.]+)/g))
    out[kebab(name)] = +value;
  for (const key of [
    'card-radius',
    'tile-radius',
    'card-padding',
    'screen-padding',
    'section-spacing',
    'hairline',
  ]) {
    if (!(key in out)) throw new Error(`tokens: Theme.swift → Metrics has no ${key}`);
  }
  return out;
}

function readMotion(theme) {
  const block = between(theme, 'enum Motion {', 'static func', 'Theme.swift → Motion');
  const out = {};
  for (const [, name, seconds] of block.matchAll(
    /static let (\w+) = Animation\.easeOut\(duration: ([\d.]+)\)/g,
  )) {
    out[kebab(name)] = Math.round(+seconds * 1000);
  }
  if (!out.quick) throw new Error('tokens: Theme.swift → Motion has no quick animation');
  return out;
}

function readType(typography, header) {
  // SwiftUI's Font.Weight → CSS font-weight.
  const CSS_WEIGHTS = {
    ultraLight: 200,
    thin: 100,
    light: 300,
    regular: 400,
    medium: 500,
    semibold: 600,
    bold: 700,
    heavy: 800,
    black: 900,
  };
  // SwiftUI's textCase → CSS text-transform. No .textCase in the label style: the text as written.
  const CSS_TEXT_CASE = { uppercase: 'uppercase', lowercase: 'lowercase' };

  const sizes = {};
  // `var size: CGFloat {` (Nocturne) or `fileprivate var size: CGFloat {` (before): both contain it.
  const sizeBlock = between(typography, 'var size: CGFloat {', '}\n    }', 'Typography.swift → size');
  for (const [, cases, value] of sizeBlock.matchAll(/case ((?:\.\w+(?:, )?)+): return ([\d.]+)/g)) {
    for (const c of cases.split(', ')) sizes[kebab(c.slice(1))] = +value;
  }
  const weights = {};
  const weightBlock = between(
    typography,
    'var weight: Font.Weight {',
    '}\n    }',
    'Typography.swift → weight',
  );
  for (const [, cases, w] of weightBlock.matchAll(/case ((?:\.\w+(?:, )?)+): return \.(\w+)/g)) {
    if (!(w in CSS_WEIGHTS)) throw new Error(`tokens: Typography.swift → weight .${w} has no CSS weight`);
    for (const c of cases.split(', ')) weights[kebab(c.slice(1))] = CSS_WEIGHTS[w];
  }
  if (!sizes.label) throw new Error('tokens: Typography.swift → size has no label');

  // The label style's own body only: tracking in points (none = 0) and its case (none = as written).
  const label = body(typography, 'func ryftLabelStyle', 'Typography.swift → ryftLabelStyle');
  const labelTracking = /\.tracking\(([\d.]+)\)/.exec(label);
  const labelCase = /\.textCase\(\.(\w+)\)/.exec(label);
  if (labelCase && !(labelCase[1] in CSS_TEXT_CASE)) {
    throw new Error(`tokens: ryftLabelStyle → .textCase(.${labelCase[1]}) has no CSS text-transform`);
  }
  // The wordmark's tracking, already in em: `static let tracking: CGFloat = 0.32` in struct Wordmark.
  const wordmark = body(header, 'struct Wordmark', 'RyftHeader.swift → Wordmark');
  const wordmarkTracking = /static let tracking: CGFloat = ([\d.]+)/.exec(wordmark);
  if (!wordmarkTracking) throw new Error('tokens: RyftHeader.swift → Wordmark.tracking not found');

  const scale = {};
  for (const [name, pt] of Object.entries(sizes)) {
    if (!(name in weights)) throw new Error(`tokens: Typography.swift → weight has no case for ${name}`);
    scale[name] = { pt, rem: +(pt / 16).toFixed(4), weight: weights[name] };
  }
  return {
    scale,
    // Tracking in em: points ÷ the label size, and the wordmark's factor as is.
    labelTrackingEm: labelTracking ? +(+labelTracking[1] / sizes.label).toFixed(4) : 0,
    labelTransform: labelCase ? CSS_TEXT_CASE[labelCase[1]] : 'none',
    wordmarkTrackingEm: +wordmarkTracking[1],
  };
}

function between(text, start, end, where) {
  const i = text.indexOf(start);
  const j = i < 0 ? -1 : text.indexOf(end, i + start.length);
  if (i < 0 || j < 0) throw new Error(`tokens: ${where} not found`);
  return text.slice(i + start.length, j);
}

/** The body of the declaration that starts at `start`: from its first `{` to the matching `}`. */
function body(text, start, where) {
  const i = text.indexOf(start);
  const open = i < 0 ? -1 : text.indexOf('{', i + start.length);
  if (open < 0) throw new Error(`tokens: ${where} not found`);
  let depth = 0;
  for (let j = open; j < text.length; j++) {
    if (text[j] === '{') depth++;
    else if (text[j] === '}' && --depth === 0) return text.slice(open + 1, j);
  }
  throw new Error(`tokens: ${where} has no closing brace`);
}

function kebab(name) {
  return name
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .replace(/([A-Z]+)([A-Z][a-z])/g, '$1-$2')
    .toLowerCase();
}

// --- output ----------------------------------------------------------------------------

function renderCss({ colors, metrics, motion, type }) {
  const names = Object.keys(colors);
  const flipping = names.filter((n) => colors[n].light !== colors[n].dark);
  const lines = [
    '/* Generated by scripts/tokens.mjs from the app (app/Ryft at the commit in web/.app-ref). Do not edit: run `npm run tokens`. */',
    flipping.length
      ? `/* ${names.length - flipping.length} colour sets have one value; ${flipping.length} have a light one, used under a light system setting. */`
      : `/* Dark-only, like the app (web D20): each of the ${names.length} colour sets has one value. */`,
    '',
    ':root {',
    '  color-scheme: dark;',
    ...names.map((n) => `  --${n}: ${colors[n].dark};`),
    '',
    ...Object.entries(metrics).map(([k, v]) => `  --${k}: ${v}px;`),
    '',
    ...Object.entries(motion).map(([k, v]) => `  --motion-${k}: ${v}ms;`),
    '',
    ...Object.entries(type.scale).flatMap(([k, v]) => [
      `  --type-${k}: ${v.rem}rem; /* ${v.pt} pt */`,
      `  --type-${k}-weight: ${v.weight};`,
    ]),
    // The label style (ryftLabelStyle): its tracking and its case.
    `  --tracking-label: ${type.labelTrackingEm}em;`,
    `  --label-transform: ${type.labelTransform};`,
    `  --tracking-wordmark: ${type.wordmarkTrackingEm}em;`,
    '}',
    '',
    // Only if a colour set has a light value of its own again (not since Nocturne).
    ...(flipping.length
      ? [
          '@media (prefers-color-scheme: light) {',
          '  :root {',
          '    color-scheme: light;',
          ...flipping.map((n) => `    --${n}: ${colors[n].light};`),
          '  }',
          '}',
          '',
        ]
      : []),
  ];
  return lines.join('\n');
}

function renderSwatches({ colors }) {
  return [
    '/* Generated by scripts/tokens.mjs: one class per colour token, for /styleguide. */',
    ...Object.keys(colors).map((n) => `.swatch-${n} {\n  background: var(--${n});\n}`),
    '',
  ].join('\n');
}

import { describe, expect, it } from 'vitest';
import { loadDenylist, normalise, redact, scanText } from '../scripts/lib/brand.mjs';

// Fixtures are ROT13, so this file never spells the name it tests for (and passes the
// repository's own brand check). rot13('jubbc') is the name in lower case.
const rot13 = (s: string) =>
  s.replace(/[a-z]/gi, (c) => {
    const base = c <= 'Z' ? 65 : 97;
    return String.fromCharCode(((c.charCodeAt(0) - base + 13) % 26) + base);
  });

const denylist = loadDenylist();
const ids = (text: string, markup = false) => scanText(text, denylist, { markup }).map((f) => f.id);
const hard = (text: string, markup = false) =>
  scanText(text, denylist, { markup }).filter((f) => f.level === 'hard');

describe('hard: the brand name', () => {
  it.each([
    ['mixed case', rot13('Gel JuBbC sbe serr')],
    ['leetspeak zeros', rot13('ju00c')],
    ['dashes', rot13('j-u-b-b-c')],
    ['dots', rot13('J.U.B.B.C.')],
    ['spaces', rot13('j u b b c')],
    ['spaced with dashes and blanks', rot13('j - u - b - b - c')],
    ['inside a longer word', rot13('jubbcvat pbhtu')],
    ['an exclamation', rot13('Jubbcf, fbzrguvat jrag jebat')],
    ['a hashtag', rot13('#jubbc')],
    ['upper case', rot13('JUBBC')],
    ['double v', rot13('iiubbc')],
  ])('catches %s', (_, text) => {
    expect(hard(text).length).toBeGreaterThan(0);
  });

  it('catches full-width letters', () => {
    const fullWidth = String.fromCodePoint(0xff57, 0xff48, 0xff4f, 0xff4f, 0xff50);
    expect(hard(fullWidth)).toHaveLength(1);
  });

  it('catches look-alike Cyrillic and Greek letters', () => {
    const lookalike =
      'w' + String.fromCodePoint(0x43d) + String.fromCodePoint(0x43e) + 'o' + String.fromCodePoint(0x440);
    expect(hard(lookalike)).toHaveLength(1);
  });

  it('catches accents and zero-width characters', () => {
    expect(hard(rot13('ju') + 'ö' + rot13('bc'))).toHaveLength(1);
    expect(hard(rot13('ju') + String.fromCodePoint(0x200b) + rot13('bbc'))).toHaveLength(1);
    expect(hard(rot13('ju') + String.fromCodePoint(0xad) + rot13('bbc'))).toHaveLength(1);
  });

  it('catches HTML entities and JavaScript escapes', () => {
    expect(hard(rot13('ju') + '&#111;' + rot13('bc'))).toHaveLength(1);
    expect(hard(rot13('ju') + '&#x6f;&#X6F;' + rot13('c'))).toHaveLength(1);
    expect(hard(rot13('ju') + '\\u006f' + rot13('bc'))).toHaveLength(1);
  });

  it('catches markup between the letters in HTML', () => {
    const html = rot13('ju') + '<b>' + rot13('bb') + '</b>' + rot13('c');
    expect(hard(html, false)).toHaveLength(0);
    expect(hard(html, true).length).toBeGreaterThan(0);
  });

  it.each([
    'RYFT is operated by its developer, who operates the servers in Frankfurt.',
    'Who opted in? Everyone who opened the app.',
    'Readiness, Recovery, Strain and Sleep from your watch.',
    'A hoop, a whip, a whopper of a night.',
    'w h o',
    'Deine ganze Gesundheit.',
  ])('leaves ordinary text alone: %s', (text) => {
    expect(hard(text)).toHaveLength(0);
  });

  it('reports the line and column', () => {
    const [f] = hard('first line\nsecond ' + rot13('jubbc'));
    expect(f).toMatchObject({ line: 2, column: 8, id: 'brand-name' });
  });

  it('masks matches in redacted text', () => {
    const masked = redact(rot13('N frys-ubfgrq Jubbc: n jngpu'), denylist);
    expect(hard(masked)).toHaveLength(0);
    expect(masked).toContain('▇▇▇▇▇');
  });
});

describe('warn: comparisons and borrowed names', () => {
  it.each([
    ['An alternative to Apple Health dashboards', 'comparison-alternative'],
    ['RYFT vs. Garmin', 'comparison-versus'],
    ['Better than Oura at night', 'comparison-than'],
    ['Unlike other trackers, RYFT …', 'comparison-others'],
    ['Like Fitbit, but private', 'comparison-like-but'],
    ['The tracker killer', 'comparison-killer'],
    ['No monthly fee, ever', 'subscription-jab'],
    ['Meet Strain Coach', 'borrowed-feature-name'],
    ['Your Healthspan, explained', 'borrowed-feature-name'],
    ['Pace of Aging', 'borrowed-feature-name'],
    ['Sleep Coach and Stress Monitor', 'borrowed-feature-name'],
    ['See your Body Age', 'borrowed-age-name'],
    ['Eine Alternative zu teuren Bändern', 'comparison-alternative'],
    ['Besser als Garmin', 'comparison-than'],
    ['Wie Oura, aber offen', 'comparison-like-but'],
    ['Keine Abo-Falle', 'subscription-jab'],
  ])('flags %s', (text, id) => {
    expect(ids(text)).toContain(id);
  });

  it.each([
    'HRV compared to your own 30-day baseline.',
    'Im Vergleich zu deinem eigenen Ausgangswert.',
    'Self-hosted in the EU, no hardware, no subscription.',
    'Works with any watch or ring that writes to Apple Health.',
    'RYFT Age, once the model ships.',
    'Better than yesterday, better than last week.',
  ])('does not flag %s', (text) => {
    expect(ids(text)).toEqual([]);
  });

  it('never lets an allow entry or warnIgnore silence a hard rule', () => {
    const allowAll = { ...denylist, allow: [{ id: 'brand-name', file: '' }], warnIgnore: [''] };
    expect(scanText(rot13('jubbc'), allowAll).some((f) => f.level === 'hard')).toBe(true);
  });
});

describe('normalise', () => {
  it('decodes entities and strips marks', () => {
    expect(normalise('caf&eacute; Ä &amp; &#246;')).toBe('caf&eacute; A & o');
  });
});

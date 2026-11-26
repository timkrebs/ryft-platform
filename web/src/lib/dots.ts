// The dot numeral's geometry (DotNumeral.swift): the glyph table and where each dot sits. Plain
// TypeScript so a test can read it against the app's table (tests/nocturne.test.ts).

/** DotGlyphs.table: 7 rows, bit (width − 1 − column), most significant bit left. */
export const GLYPHS: Record<string, { w: number; rows: number[] }> = {
  '0': { w: 5, rows: [0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110] },
  '1': { w: 5, rows: [0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110] },
  '2': { w: 5, rows: [0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111] },
  '3': { w: 5, rows: [0b01110, 0b10001, 0b00001, 0b00110, 0b00001, 0b10001, 0b01110] },
  '4': { w: 5, rows: [0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010] },
  '5': { w: 5, rows: [0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110] },
  '6': { w: 5, rows: [0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110] },
  '7': { w: 5, rows: [0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000] },
  '8': { w: 5, rows: [0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110] },
  '9': { w: 5, rows: [0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100] },
  ':': { w: 1, rows: [0, 0, 1, 0, 1, 0, 0] },
  '.': { w: 1, rows: [0, 0, 0, 0, 0, 0, 1] },
  ',': { w: 1, rows: [0, 0, 0, 0, 0, 1, 1] },
};

/** Dot 5, gap 2, so a cell is 7 apart; 7 rows are 47 high (DotNumeral.dot, dotGap, height). */
export const DOT = 5;
export const PITCH = 7;
export const DOT_HEIGHT = 47;

const n2 = (v: number) => +v.toFixed(2);
const circle = (cx: number, cy: number, r: number) =>
  `M${n2(cx - r)} ${n2(cy)}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`;

/** The lit and the unlit dots of `text` as two SVG paths, and the numeral's width. */
export function dotPaths(text: string, glyphGap: number, x0 = 0, y0 = 0) {
  let x = x0;
  let lit = '';
  let off = '';
  const glyphs = [...text].map((ch) => {
    const g = GLYPHS[ch];
    if (!g) throw new Error(`DotNumeral: no glyph for "${ch}"`);
    return g;
  });
  for (const g of glyphs) {
    for (let r = 0; r < 7; r++) {
      for (let c = 0; c < g.w; c++) {
        const on = (g.rows[r] >> (g.w - 1 - c)) & 1;
        const d = circle(x + c * PITCH + DOT / 2, y0 + r * PITCH + DOT / 2, DOT / 2);
        if (on) lit += d;
        else off += d;
      }
    }
    x += g.w * DOT + (g.w - 1) * 2 + glyphGap;
  }
  return { lit, off, width: x - glyphGap - x0 };
}

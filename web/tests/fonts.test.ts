import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// The committed Inter subsets and their licence, pinned (web D20). tests/output.test.ts checks
// that the build serves exactly these files; this pins what they are, so swapping a face for
// the full release file, another subset or another font fails here. A font change updates
// these pins and both tables in D20 in the same commit.
const FONTS = 'src/fonts';
const PINNED: Record<string, { bytes: number; sha256: string }> = {
  'Inter-Regular.woff2': {
    bytes: 18_560,
    sha256: '0fe23dbdad67636b8cf0018c9608c7c6fe870c58dfb0e06fa41d30beab890e39',
  },
  'Inter-Bold.woff2': {
    bytes: 19_216,
    sha256: '1713739f95d2e55b5a1aa463f0dca828826bd3342396ce6932d0017bd7462768',
  },
  'OFL.txt': {
    bytes: 4_380,
    sha256: '262481e844521b326f5ecd053e59b98c8b2da78c8ee1bdbb6e8174305e54935a',
  },
};
const sha256 = (path: string) => createHash('sha256').update(readFileSync(path)).digest('hex');

describe('the committed fonts (web D20)', () => {
  it('src/fonts holds exactly the pinned files', () => {
    expect(readdirSync(FONTS).sort()).toEqual(Object.keys(PINNED).sort());
  });

  for (const [file, pin] of Object.entries(PINNED)) {
    it(`${file} is the pinned file`, () => {
      expect(statSync(join(FONTS, file)).size).toBe(pin.bytes);
      expect(sha256(join(FONTS, file))).toBe(pin.sha256);
    });
  }

  it('D20 records the same hashes', () => {
    const d20 = readFileSync('docs/DECISIONS.md', 'utf8')
      .split('## D20 ')[1]!
      .split(/\n## D\d+ /)[0]!;
    for (const pin of Object.values(PINNED)) expect(d20).toContain(pin.sha256);
  });

  it('OFL.txt is the SIL Open Font License 1.1 with Inter’s copyright line', () => {
    const ofl = readFileSync(join(FONTS, 'OFL.txt'), 'utf8');
    expect(ofl).toMatch(/^Copyright \(c\) 2016 The Inter Project Authors/);
    expect(ofl).toContain('SIL OPEN FONT LICENSE Version 1.1');
  });
});

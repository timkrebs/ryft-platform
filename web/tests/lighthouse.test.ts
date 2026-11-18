import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { describe, expect, it } from 'vitest';
import rc from '../lighthouserc.json';

// The Lighthouse budgets (docs/DECISIONS.md, D24). Each page runs 3 times. The timing metrics and the
// performance score are asserted against their median over the 3 runs (LHCI's `median`), not
// against one representative run (`median-run`): with `median-run` one noisy run on CI failed `main`.
// The other assertions keep LHCI's default (`optimistic`); their values don't vary between runs.

type Result = {
  auditId: string;
  auditProperty?: string;
  name: string;
  expected: number;
  actual: number;
  values: number[];
  passed: boolean;
};

// The assertion code of the installed @lhci/cli (package.json pins it): what `lhci autorun` runs.
// If an upgrade moves this file, read its aggregation again and update D24.
const { getAllAssertionResults } = createRequire(import.meta.url)('@lhci/utils/src/assertions.js') as {
  getAllAssertionResults: (options: object, lhrs: object[]) => Result[];
};

type Run = { lcp?: number; cls?: number; tbt?: number; performance?: number };

// A minimal Lighthouse result for `/` with every audit and category the budgets read.
function lhr({ lcp = 900, cls = 0, tbt = 0, performance = 1 }: Run) {
  return {
    finalUrl: 'http://localhost:8080/',
    audits: {
      'first-contentful-paint': { score: 1, numericValue: 900 },
      interactive: { score: 1, numericValue: 900 },
      'largest-contentful-paint': { score: 1, numericValue: lcp },
      'cumulative-layout-shift': { score: 1, numericValue: cls },
      'total-blocking-time': { score: 1, numericValue: tbt },
      'resource-summary': { score: null, details: { items: [{ resourceType: 'script', transferSize: 0 }] } },
    },
    categories: {
      performance: { score: performance },
      accessibility: { score: 1 },
      'best-practices': { score: 1 },
      seo: { score: 0.92 },
    },
  };
}

const assertRuns = (runs: Run[]) =>
  getAllAssertionResults({ ...rc.ci.assert, includePassedAssertions: true }, runs.map(lhr));

describe('lighthouserc.json', () => {
  it('runs every page 3 times', () => {
    expect(rc.ci.collect.numberOfRuns).toBe(3);
  });

  it('asserts the timing metrics and the performance score against the median, every budget unchanged', () => {
    expect(rc.ci.assert.assertMatrix).toStrictEqual([
      {
        matchingUrlPattern: '.*',
        assertions: {
          'largest-contentful-paint': ['error', { maxNumericValue: 1500, aggregationMethod: 'median' }],
          'cumulative-layout-shift': ['error', { maxNumericValue: 0.05, aggregationMethod: 'median' }],
          'total-blocking-time': ['error', { maxNumericValue: 100, aggregationMethod: 'median' }],
          'resource-summary:script:size': ['error', { maxNumericValue: 81920 }],
          'categories:performance': ['error', { minScore: 0.95, aggregationMethod: 'median' }],
          'categories:accessibility': ['error', { minScore: 0.95 }],
          'categories:best-practices': ['warn', { minScore: 0.95 }],
        },
      },
      {
        matchingUrlPattern: '^http://localhost:8080/(science|beta)?$',
        assertions: { 'categories:seo': ['error', { minScore: 0.9 }] },
      },
    ]);
  });

  it('asserts nothing against one representative run', () => {
    expect(readFileSync('lighthouserc.json', 'utf8')).not.toContain('median-run');
  });
});

describe("the installed LHCI's median", () => {
  it('passes a page when one run of three misses a budget', () => {
    // CI's rerun of 2026-11-18 on `/` (TBT 1681 ms, performance 0.73 in one run) and the local
    // LCP of 1504 ms, each in a different run.
    const results = assertRuns([{ tbt: 1681, performance: 0.73 }, { lcp: 1504 }, { cls: 0.06 }]);
    expect(results.filter((r) => !r.passed)).toEqual([]);
    const tbt = results.find((r) => r.auditId === 'total-blocking-time');
    expect(tbt).toMatchObject({ actual: 0, expected: 100, values: [1681, 0, 0], passed: true });
  });

  it('fails a page when two runs of three miss a budget, at the middle value', () => {
    const failed = assertRuns([{ tbt: 162 }, { tbt: 140 }, { tbt: 0 }]).filter((r) => !r.passed);
    expect(failed).toHaveLength(1);
    expect(failed[0]).toMatchObject({
      auditId: 'total-blocking-time',
      name: 'maxNumericValue',
      actual: 140,
      expected: 100,
      values: [162, 140, 0],
    });
  });
});

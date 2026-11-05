import localFont from 'next/font/local';

// Inter 4.1 (SIL Open Font License 1.1), Regular and Bold, committed in src/fonts/ as Latin
// subsets (web D20). Not preloaded, and without next/font's computed fallback: Lighthouse's
// mobile run counts the text's repaint after a preloaded or metric-adjusted font arrives as the
// LCP (1.5 s against the 1.5 s budget), while text that paints at once in a metric-matched
// local fallback ('Inter Fallback' in globals.css, as D13 did for the previous typeface) and
// swaps later is the LCP at first paint (~1.2–1.35 s) with no layout shift. Only 400 and 700
// exist: a token weight of 500 or 600 resolves to the nearest face, never synthesised
// (`font-synthesis: none`).
export const inter = localFont({
  src: [
    { path: '../fonts/Inter-Regular.woff2', weight: '400', style: 'normal' },
    { path: '../fonts/Inter-Bold.woff2', weight: '700', style: 'normal' },
  ],
  variable: '--font-inter',
  display: 'swap',
  preload: false,
  fallback: [
    'Inter Fallback',
    'system-ui',
    '-apple-system',
    'Segoe UI',
    'Roboto',
    'Helvetica Neue',
    'Arial',
    'sans-serif',
  ],
  adjustFontFallback: false,
});

import localFont from 'next/font/local';

// Inter 4.1 (SIL Open Font License 1.1), Regular and Bold, committed in src/fonts/ as Latin
// subsets of about 19 KB (web D20). Both preloaded, and with next/font's computed fallback
// replaced by the hand-written, metric-matched 'Inter Fallback' in globals.css (as D13 did for
// the previous typeface): text paints at once with Inter's metrics and swaps without a shift.
// Measured with Lighthouse's mobile run (D20): this is the steadiest recipe — LCP 1.5–1.65 s,
// CLS 0 — where no preload swung between 1.35 and 1.8 s. Only 400 and 700 exist: a token
// weight of 500 or 600 resolves to the nearest face, never synthesised (`font-synthesis: none`).
export const inter = localFont({
  src: [
    { path: '../fonts/Inter-Regular.woff2', weight: '400', style: 'normal' },
    { path: '../fonts/Inter-Bold.woff2', weight: '700', style: 'normal' },
  ],
  variable: '--font-inter',
  display: 'swap',
  preload: true,
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

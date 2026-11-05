import localFont from 'next/font/local';

// Inter 4.1 (SIL Open Font License 1.1), Regular and Bold, committed in src/fonts/ (web D20).
// Both files are preloaded (two files, about 110 KB each); next/font computes the
// metric-matched fallback. Only 400 and 700 exist: a token weight of 500 or 600 resolves to
// the nearest face by the font-matching algorithm (500 → 400, 600 → 700), never synthesised
// (`font-synthesis: none` in globals.css).
export const inter = localFont({
  src: [
    { path: '../fonts/Inter-Regular.woff2', weight: '400', style: 'normal' },
    { path: '../fonts/Inter-Bold.woff2', weight: '700', style: 'normal' },
  ],
  variable: '--font-inter',
  display: 'swap',
  preload: true,
});

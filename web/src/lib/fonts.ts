import localFont from 'next/font/local';

// Satoshi, as in the app (Regular, Medium, Bold), self-hosted from src/fonts/
// (`npm run fonts`). Files are used exactly as supplied: no subsetting (ITF FFL).
// Not preloaded: text paints at once in the metric-matched fallback ('Satoshi Fallback' in
// globals.css), then swaps. next/font's own fallback binds only to local Arial, which Linux
// and Android don't have, so there the text reflowed when Satoshi arrived (CLS 0.17).
export const satoshi = localFont({
  src: [
    { path: '../fonts/Satoshi-Regular.woff2', weight: '400', style: 'normal' },
    { path: '../fonts/Satoshi-Medium.woff2', weight: '500', style: 'normal' },
    { path: '../fonts/Satoshi-Bold.woff2', weight: '700', style: 'normal' },
  ],
  variable: '--font-satoshi',
  display: 'swap',
  preload: false,
  fallback: [
    'Satoshi Fallback',
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

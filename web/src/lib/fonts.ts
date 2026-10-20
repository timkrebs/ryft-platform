import localFont from 'next/font/local';

// Satoshi, as in the app (Regular, Medium, Bold), self-hosted from src/fonts/
// (`npm run fonts`). Files are used exactly as supplied: no subsetting (ITF FFL).
// Not preloaded: text paints at once in the metric-matched fallback, then swaps.
export const satoshi = localFont({
  src: [
    { path: '../fonts/Satoshi-Regular.woff2', weight: '400', style: 'normal' },
    { path: '../fonts/Satoshi-Medium.woff2', weight: '500', style: 'normal' },
    { path: '../fonts/Satoshi-Bold.woff2', weight: '700', style: 'normal' },
  ],
  variable: '--font-satoshi',
  display: 'swap',
  preload: false,
  fallback: ['system-ui', '-apple-system', 'Segoe UI', 'Roboto', 'Helvetica Neue', 'Arial', 'sans-serif'],
  adjustFontFallback: 'Arial',
});

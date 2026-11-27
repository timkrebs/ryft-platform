import type { ReactNode } from 'react';
import tokens from '@/styles/tokens.json';
import { satoshi } from '@/lib/fonts';
import type { Locale } from '@/lib/i18n';
import '@/styles/tokens.css';
import '@/styles/nocturne.css';
import '@/styles/globals.css';

/**
 * <html> for the site's root layout and the 404 page. Dark-only, like the app (web D20): one
 * theme-color, the background token, and no script.
 */
export function RootDocument({ locale, children }: { locale: Locale; children: ReactNode }) {
  return (
    <html lang={locale} className={satoshi.variable}>
      <head>
        <meta name="theme-color" content={tokens.colors.background.dark} />
      </head>
      <body>{children}</body>
    </html>
  );
}

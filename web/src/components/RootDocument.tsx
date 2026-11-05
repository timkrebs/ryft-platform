import type { ReactNode } from 'react';
import tokens from '@/styles/tokens.json';
import { inter } from '@/lib/fonts';
import type { Locale } from '@/lib/i18n';
import '@/styles/tokens.css';
import '@/styles/globals.css';

/** <html> for the site's root layout and the 404 page. Light only (web D19): no theme script. */
export function RootDocument({ locale, children }: { locale: Locale; children: ReactNode }) {
  return (
    <html lang={locale} className={inter.variable}>
      <head>
        <meta name="theme-color" content={tokens.colors.background.light} />
      </head>
      <body>{children}</body>
    </html>
  );
}

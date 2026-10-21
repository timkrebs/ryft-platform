import type { ReactNode } from 'react';
import tokens from '@/styles/tokens.json';
import { satoshi } from '@/lib/fonts';
import { THEME_INIT, THEME_SWITCH } from '@/lib/inline-scripts';
import type { Locale } from '@/lib/i18n';
import '@/styles/tokens.css';
import '@/styles/globals.css';

const schemes = [
  ['dark', tokens.colors.background.dark],
  ['light', tokens.colors.background.light],
] as const;

/** <html> for both root layouts (English at /, other locales under /<locale>). */
export function RootDocument({ locale, children }: { locale: Locale; children: ReactNode }) {
  return (
    <html lang={locale} className={satoshi.variable} suppressHydrationWarning>
      <head>
        <script data-ryft="theme-init" dangerouslySetInnerHTML={{ __html: THEME_INIT }} />
        {schemes.map(([scheme, color]) => {
          const media = `(prefers-color-scheme: ${scheme})`;
          return (
            <meta
              key={scheme}
              name="theme-color"
              content={color}
              media={media}
              data-scheme={scheme}
              data-media={media}
            />
          );
        })}
      </head>
      <body>
        {children}
        <script data-ryft="theme-switch" dangerouslySetInnerHTML={{ __html: THEME_SWITCH }} />
      </body>
    </html>
  );
}

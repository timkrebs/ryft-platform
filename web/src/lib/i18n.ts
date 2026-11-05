import en from '../../messages/en.json';

// English only (web D18). The locale plumbing stays so the views read the same whether or
// not a second language comes back: `messages()`, `format()`, `localePath()` (identity).
export const locales = ['en'] as const;
export type Locale = (typeof locales)[number];

export type Messages = typeof en;
// tests/messages.test.ts checks for empty strings.
const dictionaries: Record<Locale, Messages> = { en };

export function messages(locale: Locale): Messages {
  return dictionaries[locale];
}

/** The URL path of `path` (written as the English path, e.g. `/privacy`); English lives at the root. */
export function localePath(locale: Locale, path: string): string {
  void locale;
  return path === '/' ? '/' : path;
}

/** BCP 47 tag for formatting dates and numbers. */
export function intlLocale(locale: Locale): string {
  void locale;
  return 'en-GB';
}

/** Fills `{name}` placeholders. */
export function format(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (m, k) => (k in values ? String(values[k]) : m));
}

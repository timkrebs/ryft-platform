import de from '../../messages/de.json';
import en from '../../messages/en.json';

export const locales = ['en', 'de'] as const;
export type Locale = (typeof locales)[number];
/** Locales served under a prefix (`/de/…`); English lives at the root. */
export const prefixedLocales = locales.filter((l) => l !== 'en');

export type Messages = typeof en;
// Typed: a key missing in German fails the type check; tests/messages.test.ts checks
// the other direction and empty strings.
const dictionaries: Record<Locale, Messages> = { en, de };

export function messages(locale: Locale): Messages {
  return dictionaries[locale];
}

export function isLocale(value: string): value is Locale {
  return (locales as readonly string[]).includes(value);
}

/** Static params for `app/[locale]`: every prefixed locale. */
export function prefixedLocaleParams() {
  return prefixedLocales.map((locale) => ({ locale }));
}

/** The URL path of `path` (written as the English path, e.g. `/privacy`) in `locale`. */
export function localePath(locale: Locale, path: string): string {
  const p = path === '/' ? '' : path;
  if (locale === 'en') return p || '/';
  return `/${locale}${p}`;
}

/** BCP 47 tag for formatting dates and numbers. */
export function intlLocale(locale: Locale): string {
  return locale === 'de' ? 'de-DE' : 'en-GB';
}

export function otherLocale(locale: Locale): Locale {
  return locale === 'en' ? 'de' : 'en';
}

/** Fills `{name}` placeholders. */
export function format(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (m, k) => (k in values ? String(values[k]) : m));
}

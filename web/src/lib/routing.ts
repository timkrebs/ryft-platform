import { notFound } from 'next/navigation';
import { isLocale, type Locale } from './i18n';

/** Props of a page or layout under app/[locale]. */
export type LocaleParams = { params: Promise<{ locale: string }> };

export async function localeFrom(params: LocaleParams['params']): Promise<Locale> {
  const { locale } = await params;
  if (!isLocale(locale) || locale === 'en') notFound();
  return locale;
}

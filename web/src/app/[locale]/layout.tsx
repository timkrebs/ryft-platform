import type { ReactNode } from 'react';
import { RootDocument } from '@/components/RootDocument';
import { rootMetadata } from '@/lib/metadata';
import { prefixedLocaleParams } from '@/lib/i18n';
import { localeFrom, type LocaleParams } from '@/lib/routing';

// Every other language under its prefix (/de/…). Only the generated locales exist.
export const dynamicParams = false;
export const generateStaticParams = prefixedLocaleParams;

export async function generateMetadata({ params }: LocaleParams) {
  return rootMetadata(await localeFrom(params));
}

export default async function LocaleLayout({ children, params }: LocaleParams & { children: ReactNode }) {
  return <RootDocument locale={await localeFrom(params)}>{children}</RootDocument>;
}

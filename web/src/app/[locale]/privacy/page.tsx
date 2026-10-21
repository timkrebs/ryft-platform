import { LegalPage, legalMetadata } from '@/views/Legal';
import { localeFrom, type LocaleParams } from '@/lib/routing';

export async function generateMetadata({ params }: LocaleParams) {
  return legalMetadata(await localeFrom(params), 'privacy');
}

export default async function Page({ params }: LocaleParams) {
  return <LegalPage locale={await localeFrom(params)} doc="privacy" />;
}

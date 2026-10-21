import { LegalPage, legalMetadata } from '@/views/Legal';
import { localeFrom, type LocaleParams } from '@/lib/routing';

export async function generateMetadata({ params }: LocaleParams) {
  return legalMetadata(await localeFrom(params), 'support');
}

export default async function Page({ params }: LocaleParams) {
  return <LegalPage locale={await localeFrom(params)} doc="support" />;
}

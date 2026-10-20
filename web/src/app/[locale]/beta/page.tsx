import { BetaPage, betaMetadata } from '@/views/Beta';
import { localeFrom, type LocaleParams } from '@/lib/routing';

export async function generateMetadata({ params }: LocaleParams) {
  return betaMetadata(await localeFrom(params));
}

export default async function Page({ params }: LocaleParams) {
  return <BetaPage locale={await localeFrom(params)} />;
}

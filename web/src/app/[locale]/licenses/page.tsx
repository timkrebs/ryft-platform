import { LicensesPage, licensesMetadata } from '@/views/Licenses';
import { localeFrom, type LocaleParams } from '@/lib/routing';

export async function generateMetadata({ params }: LocaleParams) {
  return licensesMetadata(await localeFrom(params));
}

export default async function Page({ params }: LocaleParams) {
  return <LicensesPage locale={await localeFrom(params)} />;
}

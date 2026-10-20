import { HomePage } from '@/views/Home';
import { pageMetadata } from '@/lib/metadata';
import { localeFrom, type LocaleParams } from '@/lib/routing';

export async function generateMetadata({ params }: LocaleParams) {
  return pageMetadata({ locale: await localeFrom(params), path: '/' });
}

export default async function Page({ params }: LocaleParams) {
  return <HomePage locale={await localeFrom(params)} />;
}

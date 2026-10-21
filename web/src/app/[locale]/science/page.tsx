import { SciencePage, scienceMetadata } from '@/views/Science';
import { localeFrom, type LocaleParams } from '@/lib/routing';

export async function generateMetadata({ params }: LocaleParams) {
  return scienceMetadata(await localeFrom(params));
}

export default async function Page({ params }: LocaleParams) {
  return <SciencePage locale={await localeFrom(params)} />;
}

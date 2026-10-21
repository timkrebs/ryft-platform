import { LegalPage, legalMetadata } from '@/views/Legal';
import { versionParams } from '@/lib/legal';
import { localeFrom } from '@/lib/routing';

type Props = { params: Promise<{ locale: string; version: string }> };

export const dynamicParams = false;
export const generateStaticParams = () => versionParams('terms');

export async function generateMetadata({ params }: Props) {
  const { version } = await params;
  return legalMetadata(await localeFrom(params), 'terms', version);
}

export default async function Page({ params }: Props) {
  const { version } = await params;
  return <LegalPage locale={await localeFrom(params)} doc="terms" version={version} />;
}

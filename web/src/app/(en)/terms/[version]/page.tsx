import { LegalPage, legalMetadata } from '@/views/Legal';
import { versionParams } from '@/lib/legal';

// Every version stays published at /terms/<version>.
type Props = { params: Promise<{ version: string }> };

export const dynamicParams = false;
export const generateStaticParams = () => versionParams('terms');

export async function generateMetadata({ params }: Props) {
  return legalMetadata('en', 'terms', (await params).version);
}

export default async function Page({ params }: Props) {
  return <LegalPage locale="en" doc="terms" version={(await params).version} />;
}

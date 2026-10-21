import { LegalPage, legalMetadata } from '@/views/Legal';
import { versionParams } from '@/lib/legal';

// Every version stays published at /privacy/<version>.
type Props = { params: Promise<{ version: string }> };

export const dynamicParams = false;
export const generateStaticParams = () => versionParams('privacy');

export async function generateMetadata({ params }: Props) {
  return legalMetadata('en', 'privacy', (await params).version);
}

export default async function Page({ params }: Props) {
  return <LegalPage locale="en" doc="privacy" version={(await params).version} />;
}

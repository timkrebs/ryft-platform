import { LegalPage, legalMetadata } from '@/views/Legal';

export const generateMetadata = () => legalMetadata('en', 'terms');

export default function Page() {
  return <LegalPage locale="en" doc="terms" />;
}

import { LegalPage, legalMetadata } from '@/views/Legal';

export const generateMetadata = () => legalMetadata('en', 'privacy');

export default function Page() {
  return <LegalPage locale="en" doc="privacy" />;
}

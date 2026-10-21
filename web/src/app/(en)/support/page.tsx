import { LegalPage, legalMetadata } from '@/views/Legal';

export const generateMetadata = () => legalMetadata('en', 'support');

export default function Page() {
  return <LegalPage locale="en" doc="support" />;
}

import { LegalPage, legalMetadata } from '@/views/Legal';

export const generateMetadata = () => legalMetadata('en', 'impressum');

export default function Page() {
  return <LegalPage locale="en" doc="impressum" />;
}

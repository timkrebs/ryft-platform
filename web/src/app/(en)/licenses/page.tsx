import { LicensesPage, licensesMetadata } from '@/views/Licenses';

export const metadata = licensesMetadata('en');

export default function Page() {
  return <LicensesPage locale="en" />;
}

import { BetaPage, betaMetadata } from '@/views/Beta';

export const metadata = betaMetadata('en');

export default function Page() {
  return <BetaPage locale="en" />;
}

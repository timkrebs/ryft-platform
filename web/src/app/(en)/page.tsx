import { HomePage } from '@/views/Home';
import { pageMetadata } from '@/lib/metadata';

export const metadata = pageMetadata({ locale: 'en', path: '/' });

export default function Page() {
  return <HomePage locale="en" />;
}

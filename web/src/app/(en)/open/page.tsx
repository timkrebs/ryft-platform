import type { Metadata } from 'next';
import { OpenPage } from '@/views/Open';
import { messages } from '@/lib/i18n';

// out/open.html: nginx serves it for the app's universal-link paths (nginx/default.conf).
export const metadata: Metadata = {
  title: messages('en').open.title,
  robots: { index: false, follow: false },
  referrer: 'no-referrer',
};

export default function Page() {
  return <OpenPage />;
}

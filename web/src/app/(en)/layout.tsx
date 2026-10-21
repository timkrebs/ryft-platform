import type { ReactNode } from 'react';
import { RootDocument } from '@/components/RootDocument';
import { rootMetadata } from '@/lib/metadata';

// English, at the root: the URLs the app links to (/privacy, /terms, /support, …).
export const metadata = rootMetadata('en');

export default function EnglishLayout({ children }: { children: ReactNode }) {
  return <RootDocument locale="en">{children}</RootDocument>;
}

import type { Metadata } from 'next';
import { NotFoundPage } from '@/views/NotFound';
import { RootDocument } from '@/components/RootDocument';
import { messages } from '@/lib/i18n';
import { rootMetadata } from '@/lib/metadata';

// out/404.html, for every unknown URL (Firebase Hosting serves it on a 404, web D19).
export const metadata: Metadata = {
  ...rootMetadata('en'),
  title: { absolute: `${messages('en').notFound.title} · RYFT` },
  robots: { index: false, follow: true },
};

export default function GlobalNotFound() {
  return (
    <RootDocument locale="en">
      <NotFoundPage />
    </RootDocument>
  );
}

import { StyleguidePage } from '@/views/Styleguide';
import { pageMetadata } from '@/lib/metadata';
import { messages } from '@/lib/i18n';

// Tokens and components on one page. English only, noindex, not in the sitemap.
export const metadata = pageMetadata({
  locale: 'en',
  path: '/styleguide',
  title: messages('en').styleguide.title,
  description: messages('en').styleguide.description,
  noindex: true,
  translated: false,
});

export default function Page() {
  return <StyleguidePage />;
}

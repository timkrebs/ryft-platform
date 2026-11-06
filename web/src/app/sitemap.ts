import type { MetadataRoute } from 'next';
import { localePath } from '@/lib/i18n';
import { SINGLE, VERSIONED, loadLegal } from '@/lib/legal';
import { absoluteUrl } from '@/lib/site';

export const dynamic = 'force-static';

// Published pages only: no drafts, no styleguide, no app-link fallback, no 404.
export default async function sitemap(): Promise<MetadataRoute.Sitemap> {
  const pages = ['/', '/science', '/beta', '/licenses'];
  for (const doc of [...VERSIONED, ...SINGLE]) {
    const { data } = await loadLegal('en', doc);
    if (data.status === 'published') pages.push(`/${doc}`);
  }
  return pages.map((path) => ({ url: absoluteUrl(localePath('en', path)) }));
}

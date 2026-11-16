import type { Metadata } from 'next';
import { localePath, messages, type Locale } from './i18n';
import { SITE_NAME, SITE_URL, absoluteUrl } from './site';

/** Shared by the root layouts. */
export function rootMetadata(locale: Locale): Metadata {
  const t = messages(locale);
  return {
    metadataBase: new URL(SITE_URL),
    title: { default: `${SITE_NAME} — ${t.site.tagline}`, template: `%s · ${SITE_NAME}` },
    description: t.site.description,
    applicationName: SITE_NAME,
    icons: {
      icon: [
        { url: '/favicon.svg', type: 'image/svg+xml' },
        { url: '/favicon-32.png', sizes: '32x32', type: 'image/png' },
      ],
      apple: [{ url: '/apple-touch-icon.png', sizes: '180x180' }],
    },
    manifest: '/site.webmanifest',
    formatDetection: { telephone: false, email: false, address: false },
  };
}

type PageMeta = {
  locale: Locale;
  /** The English path, e.g. `/privacy`. */
  path: string;
  title?: string;
  description?: string;
  /** Drafts, the styleguide, the app-link fallback and 404 stay out of search engines. */
  noindex?: boolean;
};

/** Title, description, canonical, Open Graph and Twitter for a page. English only (web D18). */
export function pageMetadata({ locale, path, title, description, noindex }: PageMeta): Metadata {
  const t = messages(locale);
  const url = absoluteUrl(localePath(locale, path));
  const ogTitle = title ? `${title} · ${SITE_NAME}` : `${SITE_NAME} — ${t.site.tagline}`;
  const desc = description ?? t.site.description;
  const image = {
    url: `/og/${locale}.png`,
    width: 1200,
    height: 630,
    alt: `${SITE_NAME} — ${t.site.tagline}`,
  };
  return {
    title: title ?? { absolute: `${SITE_NAME} — ${t.site.tagline}` },
    description: desc,
    alternates: { canonical: url },
    openGraph: {
      type: 'website',
      siteName: SITE_NAME,
      url,
      title: ogTitle,
      description: desc,
      locale: 'en_GB',
      images: [image],
    },
    twitter: { card: 'summary_large_image', title: ogTitle, description: desc, images: [image.url] },
    robots: noindex ? { index: false, follow: true } : undefined,
  };
}

// Site-wide constants. Values from the environment are read at build time and baked
// into the static HTML (docs/README → "Build-time settings").

export const SITE_URL = 'https://ryftapp.de';
export const SITE_NAME = 'RYFT';

/** The public TestFlight link. While unset, every "Open in TestFlight" button is hidden. */
export const TESTFLIGHT_URL = clean(process.env.NEXT_PUBLIC_TESTFLIGHT_URL);

/**
 * The support address (mailto links on /beta and /support). While unset, the pages show
 * the same TODO(user) marker as the legal drafts.
 */
export const SUPPORT_EMAIL = clean(process.env.NEXT_PUBLIC_SUPPORT_EMAIL);

/** The year in the footer: the build's year. */
export const BUILD_YEAR = new Date().getUTCFullYear();

function clean(value: string | undefined): string | null {
  const v = value?.trim();
  return v ? v : null;
}

/** `https://ryftapp.de/…` for a site path; the home page keeps its slash. */
export function absoluteUrl(path: string): string {
  return path === '/' ? `${SITE_URL}/` : `${SITE_URL}${path}`;
}

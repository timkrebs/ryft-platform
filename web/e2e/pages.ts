// Every page of the site (English only, web D18).
export const PAGES: { path: string }[] = [
  { path: '/' },
  { path: '/science' },
  { path: '/beta' },
  { path: '/privacy' },
  { path: '/privacy/2026-10-20' },
  { path: '/terms' },
  { path: '/terms/2026-10-20' },
  { path: '/impressum' },
  { path: '/support' },
  { path: '/licenses' },
  { path: '/styleguide' },
  { path: '/open' },
];

export const ALL = PAGES.map((p) => p.path);

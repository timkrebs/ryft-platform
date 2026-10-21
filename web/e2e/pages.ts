// Every page, as the English path; German exists under /de unless `en` says otherwise.
export const PAGES: { path: string; enOnly?: boolean }[] = [
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
  { path: '/styleguide', enOnly: true },
  { path: '/open', enOnly: true },
];

export const de = (path: string) => (path === '/' ? '/de' : `/de${path}`);

export const ALL = PAGES.flatMap((p) => (p.enOnly ? [p.path] : [p.path, de(p.path)]));

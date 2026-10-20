import { defineConfig, globalIgnores } from 'eslint/config';
import nextVitals from 'eslint-config-next/core-web-vitals';
import nextTs from 'eslint-config-next/typescript';

export default defineConfig([
  ...nextVitals,
  ...nextTs,
  {
    rules: {
      // Pages are static HTML without the Next runtime (docs/DECISIONS.md, D2): plain
      // links are the norm, and a client component would ship nothing.
      '@next/next/no-html-link-for-pages': 'off',
    },
  },
  globalIgnores([
    '.next/**',
    'out/**',
    'next-env.d.ts',
    'src/generated/**',
    'test-results/**',
    'playwright-report/**',
    '.lighthouseci/**',
  ]),
]);

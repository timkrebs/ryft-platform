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
      // <head> in an App Router root layout (RootDocument) is the documented way.
      '@next/next/no-head-element': 'off',
    },
  },
  {
    // The admin page (web D15): one inline script, no HTML sinks. Trusted Types 'none' refuses
    // them at run time, tests/admin-source.test.ts scans for them, and this rule stops them
    // in the editor.
    files: ['src/admin/**/*.ts'],
    rules: {
      'no-console': 'error',
      'no-restricted-properties': [
        'error',
        { property: 'innerHTML', message: 'Build DOM with h() and text nodes (src/admin/dom.ts).' },
        { property: 'outerHTML', message: 'Build DOM with h() and text nodes (src/admin/dom.ts).' },
        { property: 'insertAdjacentHTML', message: 'Build DOM with h() and text nodes (src/admin/dom.ts).' },
        { property: 'srcdoc', message: 'No framed documents.' },
        { object: 'document', property: 'write', message: 'No document.write.' },
        { object: 'document', property: 'writeln', message: 'No document.write.' },
        { object: 'document', property: 'cookie', message: 'No cookies.' },
      ],
      'no-restricted-globals': [
        'error',
        { name: 'localStorage', message: 'The session lives in sessionStorage only (web D16).' },
        { name: 'XMLHttpRequest', message: 'fetch only, to the two configured origins.' },
        { name: 'WebSocket', message: 'The CSP allows no WebSocket.' },
      ],
      'no-restricted-syntax': [
        'error',
        { selector: "CallExpression[callee.name='eval']", message: 'No eval.' },
        { selector: "NewExpression[callee.name='Function']", message: 'No new Function.' },
        {
          selector:
            'CallExpression[callee.name=/^(setTimeout|setInterval)$/][arguments.0.type=/^(Literal|TemplateLiteral)$/]',
          message: 'No string timers.',
        },
        {
          selector:
            'CallExpression[callee.property.name=/^(setTimeout|setInterval)$/][arguments.0.type=/^(Literal|TemplateLiteral)$/]',
          message: 'No string timers.',
        },
      ],
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

import type { Metadata } from 'next';
import type { ReactNode } from 'react';
import { satoshi } from '@/lib/fonts';
import '@/styles/tokens.css';
import '@/styles/globals.css';
import '@/admin/admin.css';

// The operator's page has its own document (web D15): no site header or footer, no theme
// scripts (the tokens follow the system appearance), and noindex. Its one script is inlined
// by the page.
export const metadata: Metadata = {
  title: 'RYFT admin',
  robots: { index: false, follow: false },
  referrer: 'no-referrer',
};

export default function AdminLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" className={satoshi.variable}>
      <body>{children}</body>
    </html>
  );
}

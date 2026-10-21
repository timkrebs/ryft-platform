// Components for MDX documents (science, legal): typography for long text, formulas as
// MathML rendered at build time (no math JavaScript, readable by screen readers), and
// the TODO(user) marker of the legal drafts.
import type { MDXComponents } from 'mdx/types';
import type { ReactNode } from 'react';
import temml from 'temml';
import { Todo } from './ui';
import styles from './prose.module.css';

/** A display formula. MathML is native in current browsers and read by screen readers. */
export function Formula({ tex }: { tex: string }) {
  const mathml = temml
    .renderToString(tex, { displayMode: true, annotate: true, throwOnError: true })
    // Temml sets display:block math inline; the stylesheet does it (CSP: no inline styles).
    .replace(/\s+style="[^"]*"/g, '');
  // Wide formulas scroll sideways inside their box, which must then be reachable by keyboard.
  return <div className={styles.formula} tabIndex={0} dangerouslySetInnerHTML={{ __html: mathml }} />;
}

/** The long-text frame: measure, rhythm, tables and links in the app's tokens. */
export function Prose({ children }: { children: ReactNode }) {
  return <div className={styles.prose}>{children}</div>;
}

export const mdxComponents: MDXComponents = {
  Formula,
  Todo,
  table: (props) => (
    <div className={styles.tableWrap}>
      <table {...props} />
    </div>
  ),
};

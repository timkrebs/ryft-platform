// Small building blocks of the monochrome system (web/docs/DECISIONS.md, "Alpine"): hairline
// sections with a numbered eyebrow, square buttons with the ↗ arrow, tracked uppercase labels,
// the black ticker band. Colours and motion are tokens from the app; the web type scale and
// rhythm are the `--web-*` properties in globals.css.
import type { ComponentPropsWithoutRef, ReactNode } from 'react';
import styles from './ui.module.css';

const cx = (...names: (string | false | undefined | null)[]) => names.filter(Boolean).join(' ');

/** Small uppercase tracked label (the app's ryftLabelStyle; here Inter 400, +0.08 em). */
export function Label({
  children,
  as: Tag = 'p',
  tone = 'secondary',
  className,
  id,
}: {
  children: ReactNode;
  as?: 'p' | 'span' | 'h2' | 'h3' | 'dt';
  tone?: 'secondary' | 'primary' | 'accent' | 'warning' | 'alert' | 'inverse';
  className?: string;
  id?: string;
}) {
  return (
    <Tag id={id} className={cx(styles.label, styles[`tone-${tone}`], className)}>
      {children}
    </Tag>
  );
}

/** The section eyebrow: `02 / SCORES`, in the primary ink. */
export function Eyebrow({
  children,
  as: Tag = 'p',
  tone = 'primary',
  className,
  id,
}: {
  children: ReactNode;
  as?: 'p' | 'span' | 'h2' | 'h3';
  tone?: 'primary' | 'secondary' | 'inverse';
  className?: string;
  id?: string;
}) {
  return (
    <Tag id={id} className={cx(styles.eyebrow, styles[`tone-${tone}`], className)}>
      {children}
    </Tag>
  );
}

export function Card({
  children,
  as: Tag = 'div',
  className,
  id,
  labelledBy,
}: {
  children: ReactNode;
  as?: 'div' | 'section' | 'article' | 'li' | 'figure';
  className?: string;
  id?: string;
  labelledBy?: string;
}) {
  return (
    <Tag id={id} aria-labelledby={labelledBy} className={cx(styles.card, className)}>
      {children}
    </Tag>
  );
}

export function Tile({
  children,
  as: Tag = 'div',
  className,
}: {
  children: ReactNode;
  as?: 'div' | 'li' | 'article';
  className?: string;
}) {
  return <Tag className={cx(styles.tile, className)}>{children}</Tag>;
}

/**
 * A page section: a hairline on top, the numbered eyebrow (`02 / SCORES`), the heading in
 * display caps, an optional intro, then the content.
 */
export function Section({
  id,
  number,
  label,
  title,
  intro,
  children,
  className,
  headingLevel = 2,
  tone = 'default',
}: {
  id?: string;
  number?: string;
  label?: string;
  title?: ReactNode;
  intro?: ReactNode;
  children?: ReactNode;
  className?: string;
  headingLevel?: 1 | 2;
  tone?: 'default' | 'inverse';
}) {
  const H = headingLevel === 1 ? 'h1' : 'h2';
  const headingId = id ? `${id}-title` : undefined;
  return (
    <section
      id={id}
      aria-labelledby={title ? headingId : undefined}
      className={cx(styles.section, tone === 'inverse' && styles.sectionInverse, className)}
    >
      {(label || title || intro) && (
        <header className={styles.sectionHeader}>
          {label && (
            <Eyebrow tone={tone === 'inverse' ? 'inverse' : 'primary'}>
              {number ? `${number} / ${label}` : label}
            </Eyebrow>
          )}
          {title && (
            <H id={headingId} className={styles.sectionTitle}>
              {title}
            </H>
          )}
          {intro && <div className={styles.intro}>{intro}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

/** A link styled as the square button; `arrow` appends the ↗ of every call to action. */
export function ButtonLink({
  href,
  children,
  variant = 'primary',
  arrow = false,
  className,
  ...rest
}: {
  href: string;
  children: ReactNode;
  variant?: 'primary' | 'secondary' | 'quiet' | 'inverse' | 'inverseOutline';
  arrow?: boolean;
  className?: string;
} & Omit<ComponentPropsWithoutRef<'a'>, 'href' | 'className' | 'children'>) {
  return (
    <a href={href} className={cx(styles.button, styles[`button-${variant}`], className)} {...rest}>
      <span>{children}</span>
      {arrow && (
        <span className={styles.arrow} aria-hidden="true">
          ↗
        </span>
      )}
    </a>
  );
}

/** Content width and the screen padding. */
export function Container({
  children,
  className,
  narrow,
}: {
  children: ReactNode;
  className?: string;
  narrow?: boolean;
}) {
  return <div className={cx(styles.container, narrow && styles.narrow, className)}>{children}</div>;
}

/**
 * The black ticker band. The text is repeated to fill the band; the copies are hidden from
 * assistive technology. It moves once, for four seconds, and not at all under Reduce Motion.
 */
export function Marquee({ text }: { text: string }) {
  // Four copies: enough to fill the band at any width the design allows, also after it has
  // moved by one copy (ui.module.css: one run of four seconds, none under Reduce Motion).
  const copies = 4;
  return (
    <div className={styles.marquee} role="presentation">
      <p className={styles.marqueeTrack}>
        {Array.from({ length: copies }, (_, i) => (
          <span key={i} aria-hidden={i > 0 ? true : undefined}>
            {text}
          </span>
        ))}
      </p>
    </div>
  );
}

/** The marker for an unfinished value, styled like the legal drafts' TODO(user). */
export function Todo({ children }: { children: ReactNode }) {
  return <mark className={styles.todo}>{children}</mark>;
}

/** "Sample data" note for figures that aren't real measurements. */
export function SampleNote({ children }: { children: ReactNode }) {
  return <p className={styles.sample}>{children}</p>;
}

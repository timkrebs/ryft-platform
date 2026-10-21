// Small building blocks with the app's shapes (Theme.swift → Metrics): cards (radius 18,
// padding 18), tiles (radius 16), sections (24 apart), pill buttons, tracked labels.
import type { ComponentPropsWithoutRef, ReactNode } from 'react';
import styles from './ui.module.css';

const cx = (...names: (string | false | undefined)[]) => names.filter(Boolean).join(' ');

/** Small uppercase tracked label (ryftLabelStyle). */
export function Label({
  children,
  as: Tag = 'p',
  tone = 'secondary',
  className,
  id,
}: {
  children: ReactNode;
  as?: 'p' | 'span' | 'h2' | 'h3' | 'dt';
  tone?: 'secondary' | 'accent' | 'warning' | 'alert';
  className?: string;
  id?: string;
}) {
  return (
    <Tag id={id} className={cx(styles.label, styles[`tone-${tone}`], className)}>
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

/** A page section: optional label above the heading, an intro, and content. */
export function Section({
  id,
  label,
  title,
  intro,
  children,
  className,
  headingLevel = 2,
}: {
  id?: string;
  label?: string;
  title?: ReactNode;
  intro?: ReactNode;
  children?: ReactNode;
  className?: string;
  headingLevel?: 1 | 2;
}) {
  const H = headingLevel === 1 ? 'h1' : 'h2';
  const headingId = id ? `${id}-title` : undefined;
  return (
    <section
      id={id}
      aria-labelledby={title ? headingId : undefined}
      className={cx(styles.section, className)}
    >
      {(label || title || intro) && (
        <header className={styles.sectionHeader}>
          {label && <Label as="p">{label}</Label>}
          {title && <H id={headingId}>{title}</H>}
          {intro && <div className={styles.intro}>{intro}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

/** A link styled as the app's pill button. */
export function ButtonLink({
  href,
  children,
  variant = 'primary',
  className,
  ...rest
}: {
  href: string;
  children: ReactNode;
  variant?: 'primary' | 'secondary' | 'quiet';
  className?: string;
} & Omit<ComponentPropsWithoutRef<'a'>, 'href' | 'className' | 'children'>) {
  return (
    <a href={href} className={cx(styles.button, styles[`button-${variant}`], className)} {...rest}>
      {children}
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

/** The marker for an unfinished value, styled like the legal drafts' TODO(user). */
export function Todo({ children }: { children: ReactNode }) {
  return <mark className={styles.todo}>{children}</mark>;
}

/** "Sample data" note for figures that aren't real measurements. */
export function SampleNote({ children }: { children: ReactNode }) {
  return <p className={styles.sample}>{children}</p>;
}

// Small building blocks with the app's shapes (Theme.swift → Metrics, Card.swift, PillButton.swift,
// GroupedList.swift, IconDisc.swift): the neutral card (radius 22, 16 inside), the neutral tile,
// the pill button, the grouped list and the icon disc. web/docs/NOCTURNE.md says where each comes from.
import type { ComponentPropsWithoutRef, ReactNode } from 'react';
import { ArrowRight, ArrowUpRight, Chevron } from './icons';
import styles from './ui.module.css';

const cx = (...names: (string | false | undefined)[]) => names.filter(Boolean).join(' ');

/** A section or tile label (ryftLabelStyle): its case and tracking come from the tokens. */
export function Label({
  children,
  as: Tag = 'p',
  tone = 'secondary',
  className,
  id,
}: {
  children: ReactNode;
  as?: 'p' | 'span' | 'h2' | 'h3' | 'dt';
  /** Ink, or a severity word's own colour (an insight's "Notice" is amber, "Attention" coral). */
  tone?: 'secondary' | 'tertiary' | 'warning' | 'alert';
  className?: string;
  id?: string;
}) {
  return (
    <Tag id={id} className={cx(styles.label, styles[`tone-${tone}`], className)}>
      {children}
    </Tag>
  );
}

/** The neutral card: `surface`, radius 22, 16 inside, no border and no shadow (Card.swift). */
export function Card({
  children,
  as: Tag = 'div',
  className,
  id,
  labelledBy,
}: {
  children: ReactNode;
  as?: 'div' | 'section' | 'article' | 'li' | 'figure' | 'aside';
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

/** The neutral tile (NeutralTile.swift): a label top left, a Light value bottom left, 80 high. */
export function Tile({
  label,
  value,
  unit,
  note,
  className,
}: {
  label: string;
  value: string;
  unit?: string;
  note?: string;
  className?: string;
}) {
  return (
    <div className={cx(styles.tile, className)}>
      <p className={styles.tileLabel}>{label}</p>
      <p className={styles.tileValueLine}>
        <span className={styles.tileValue}>{value}</span>
        {unit && <span className={styles.tileUnit}>{unit}</span>}
        {note && <span className={styles.tileNote}>{note}</span>}
      </p>
    </div>
  );
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

/**
 * The app's button as a link (PillButton.swift): `primary` is the light pill with the title leading
 * and a dark 40 px circle with a glyph trailing; `secondary` an outline pill; `quiet` the text link
 * (ink, underlined at 50 %). 52 px high at least, the whole pill is the target.
 */
export function ButtonLink({
  href,
  children,
  variant = 'primary',
  icon = 'forward',
  className,
  ...rest
}: {
  href: string;
  children: ReactNode;
  variant?: 'primary' | 'secondary' | 'quiet';
  /** The primary pill's circle: `forward` (go on, the default) or `out` (a link out of the site). */
  icon?: 'forward' | 'out';
  className?: string;
} & Omit<ComponentPropsWithoutRef<'a'>, 'href' | 'className' | 'children'>) {
  return (
    <a href={href} className={cx(styles.button, styles[`button-${variant}`], className)} {...rest}>
      <span>{children}</span>
      {variant === 'primary' && (
        <span className={styles.circle}>{icon === 'out' ? <ArrowUpRight /> : <ArrowRight />}</span>
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

/** The marker for an unfinished value, styled like the legal drafts' TODO(user). */
export function Todo({ children }: { children: ReactNode }) {
  return <mark className={styles.todo}>{children}</mark>;
}

/** "Sample data" note for figures that aren't real measurements. */
export function SampleNote({ children }: { children: ReactNode }) {
  return <p className={styles.sample}>{children}</p>;
}

/**
 * The grouped list (GroupedList.swift): an optional title (13, tertiary), a `surface` card of rows with
 * a hairline between them, an optional footer (12, tertiary). `GroupRow` is a row, `GroupLink` a row
 * that is a link (a chevron, or the arrow out for an outside link).
 */
export function Group({
  title,
  titleId,
  footer,
  children,
  as: Tag = 'ul',
  className,
}: {
  title?: string;
  titleId?: string;
  footer?: ReactNode;
  children: ReactNode;
  as?: 'ul' | 'ol' | 'div';
  className?: string;
}) {
  return (
    <div className={cx(styles.group, className)}>
      {title && (
        <p id={titleId} className={styles.groupTitle}>
          {title}
        </p>
      )}
      <Tag className={styles.groupCard} aria-labelledby={title ? titleId : undefined}>
        {children}
      </Tag>
      {footer && <div className={styles.groupFooter}>{footer}</div>}
    </div>
  );
}

export function GroupRow({
  children,
  as: Tag = 'li',
  className,
}: {
  children: ReactNode;
  as?: 'li' | 'div';
  className?: string;
}) {
  return <Tag className={cx(styles.row, className)}>{children}</Tag>;
}

export function GroupLink({
  href,
  children,
  detail,
  out,
  current,
}: {
  href: string;
  children: ReactNode;
  detail?: string;
  out?: boolean;
  current?: boolean;
}) {
  return (
    <li className={styles.rowLi}>
      <a href={href} className={styles.rowLink} aria-current={current ? 'page' : undefined}>
        <span className={styles.rowText}>
          <span>{children}</span>
          {detail && <span className={styles.rowDetail}>{detail}</span>}
        </span>
        <span className={styles.accessory}>{out ? <ArrowUpRight size={15} /> : <Chevron size={12} />}</span>
      </a>
    </li>
  );
}

/** A 36 px disc with a thin ink glyph in a faint ring, where a thing needs an icon (IconDisc.swift). */
export function IconDisc({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cx(styles.disc, className)} aria-hidden="true">
      {children}
    </span>
  );
}

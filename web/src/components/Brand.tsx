import { Mark } from './Mark';
import styles from './Brand.module.css';

/** "RYFT" in Satoshi Light, tracked 0.32 em, as the app's Wordmark (RyftHeader.swift). */
export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={`${styles.wordmark} ${className ?? ''}`} translate="no">
      RYFT
    </span>
  );
}

/**
 * The lock-up: the orbit mark, 10 px of air at 26, then the wordmark at 15 (WordmarkLockup in the
 * app); both scale with `size`, the mark's side in px.
 */
export function Logo({ size = 26 }: { size?: number }) {
  const scale = size >= 40 ? 'large' : size > 26 ? 'medium' : 'small';
  return (
    <span className={styles.logo} data-size={scale}>
      <Mark size={size} />
      <Wordmark />
    </span>
  );
}

/** The lock-up with the tagline (BrandLockup in the app), for the footer. */
export function Lockup({ tagline, as: Tag = 'p' }: { tagline: string; as?: 'p' | 'h1' }) {
  return (
    <div className={styles.lockup}>
      <span className={styles.lockupRow} aria-hidden="true">
        <Mark size={44} />
        <Wordmark className={styles.lockupWordmark} />
      </span>
      <Tag className={styles.tagline}>{tagline}</Tag>
    </div>
  );
}

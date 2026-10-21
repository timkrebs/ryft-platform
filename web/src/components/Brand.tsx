import { Mark } from './Mark';
import styles from './Brand.module.css';

/** "RYFT" in wide-tracked Satoshi, as the app's Wordmark (RyftHeader.swift). */
export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={`${styles.wordmark} ${className ?? ''}`} translate="no">
      RYFT
    </span>
  );
}

/** Mark + wordmark on one line, sized from the mark like the app's RyftHeader. */
export function Logo({ size = 22 }: { size?: number }) {
  return (
    <span className={styles.logo} data-size={size >= 40 ? 'large' : 'small'}>
      <Mark size={size} />
      <Wordmark />
    </span>
  );
}

/** The lock-up with the tagline (BrandLockup in the app), for the hero and the footer. */
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

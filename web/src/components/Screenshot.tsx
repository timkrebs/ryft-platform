import { getImageProps } from 'next/image';
import shots from '@/data/screenshots.json';
import styles from './Screenshot.module.css';

export type ShotId = keyof typeof shots;

type Props = {
  id: ShotId;
  /** What the screenshot shows, for screen readers. */
  alt: string;
  /** The `sizes` attribute: how wide the image is drawn. */
  sizes: string;
  priority?: boolean;
  className?: string;
};

/**
 * A real capture from the app (scripts/screenshots.mjs): the light capture in light mode,
 * the dark one otherwise; AVIF, then WebP, then PNG. The theme switch retargets the
 * <source> media (`data-scheme`, `data-media`) when the visitor overrides the system.
 */
export function Screenshot({ id, alt, sizes, priority = false, className }: Props) {
  const shot = shots[id];
  const file = (scheme: string, w: number, ext: string) => `/screenshots/${id}-${scheme}-${w}.${ext}`;
  const srcSet = (scheme: string, ext: string) =>
    shot.widths.map((w) => `${file(scheme, w, ext)} ${w}w`).join(', ');
  // next/image for the <img> attributes (dimensions, loading, decoding, priority); the
  // sizes are pre-generated, so it runs unoptimized. Its inline style is dropped (CSP).
  const { props } = getImageProps({
    src: `/screenshots/${id}-dark.png`,
    width: shot.width,
    height: shot.height,
    alt,
    sizes,
    priority,
    unoptimized: true,
  });
  const { style: _style, ...img } = props;
  void _style;
  const sources = [
    ['light', '(prefers-color-scheme: light)'],
    ['dark', 'all'],
  ] as const;
  return (
    <picture className={className}>
      {sources.flatMap(([scheme, media]) =>
        (['avif', 'webp'] as const).map((ext) => (
          <source
            key={`${scheme}-${ext}`}
            type={`image/${ext}`}
            srcSet={srcSet(scheme, ext)}
            sizes={sizes}
            media={media}
            data-scheme={scheme}
            data-media={media}
          />
        )),
      )}
      {/* eslint-disable-next-line jsx-a11y/alt-text -- alt comes from getImageProps */}
      <img {...img} className={styles.img} />
    </picture>
  );
}

/** A phone outline drawn in CSS around a full-screen capture (no device artwork). */
export function DeviceFrame({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    <div className={`${styles.device} ${className ?? ''}`}>
      <div className={styles.body}>
        <div className={styles.screen}>{children}</div>
      </div>
    </div>
  );
}

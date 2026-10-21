import { markGeometry } from '@/lib/mark';

type Props = {
  /** CSS size in px (width = height). */
  size?: number;
  showsInnerOrbit?: boolean;
  className?: string;
  /** Accessible name; decorative (hidden) when omitted. */
  title?: string;
};

/** The orbit mark (RyftMark.swift), in the current text colour. */
export function Mark({ size = 24, showsInnerOrbit = true, className, title }: Props) {
  const g = markGeometry(showsInnerOrbit);
  return (
    <svg
      viewBox={`0 0 ${g.size} ${g.size}`}
      width={size}
      height={size}
      className={className}
      role={title ? 'img' : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      <g fill="none" stroke="currentColor" strokeWidth={g.stroke} strokeLinecap="round">
        {g.arcs.map((d) => (
          <path key={d} d={d} />
        ))}
        {g.inner && <circle cx={g.inner.cx} cy={g.inner.cy} r={g.inner.r} />}
      </g>
      <g fill="currentColor">
        {g.dots.map((d) => (
          <circle key={`${d.cx},${d.cy}`} cx={d.cx} cy={d.cy} r={d.r} />
        ))}
      </g>
    </svg>
  );
}

// The few glyphs the pages need, drawn as the app's SF Symbols read at 14–16 pt: a thin ink
// stroke, round ends. Decorative: every one is hidden from assistive technology, because the text
// next to it says what it is.
type GlyphProps = { size?: number; className?: string };

function Glyph({ size = 14, className, children }: GlyphProps & { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      className={className}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  );
}

/** `arrow.right`: Continue, Join the beta, a plan row's start. */
export const ArrowRight = (p: GlyphProps) => (
  <Glyph {...p}>
    <path d="M2.5 8h11M9 3.5 13.5 8 9 12.5" />
  </Glyph>
);

/** `arrow.up.right`: any link out of the page. */
export const ArrowUpRight = (p: GlyphProps) => (
  <Glyph {...p}>
    <path d="M4 12 12 4M5.5 4H12v6.5" />
  </Glyph>
);

/** `chevron.right`: a row that opens something. */
export const Chevron = (p: GlyphProps) => (
  <Glyph {...p}>
    <path d="m6 3 5 5-5 5" />
  </Glyph>
);

/** `checkmark`: a set that is done. */
export const Check = (p: GlyphProps) => (
  <Glyph {...p}>
    <path d="m3 8.5 3.2 3.2L13 4.5" />
  </Glyph>
);

/** `moon`, `heart`, `bolt`: the three metrics, on an icon disc. */
export const MoonGlyph = (p: GlyphProps) => (
  <Glyph {...p} size={p.size ?? 16}>
    <path d="M10.5 2.2a6 6 0 1 0 3.3 10.6A6.3 6.3 0 0 1 10.5 2.2Z" />
  </Glyph>
);

export const PulseGlyph = (p: GlyphProps) => (
  <Glyph {...p} size={p.size ?? 16}>
    <path d="M1.5 8.5h3l1.6-4 3 8 1.9-5.5 1 1.5h2.5" />
  </Glyph>
);

export const BoltGlyph = (p: GlyphProps) => (
  <Glyph {...p} size={p.size ?? 16}>
    <path d="M9 1.8 3.7 9h4l-1 5.2L12.3 7h-4L9 1.8Z" />
  </Glyph>
);

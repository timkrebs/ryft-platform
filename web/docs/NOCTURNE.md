# Nocturne on the web

The site's design language, derived from the app. **No web spec existed**: web D20 took only the tokens
("no design change beyond what they carry"), and no draft, branch or document described a Nocturne site.
This file is that spec, written from the app at the commit in `web/.app-ref` (`ea52758`, app main at #142,
which includes #117 "today and sleep in nocturne" and #131 "onboarding, train and settings in nocturne").
It is not the app's spec: it says what each app rule becomes in static HTML and CSS, or why it doesn't apply.
web D26 records the decision; `tests/nocturne.test.ts` pins the numbers that tokens.css doesn't carry.

**Sources** (paths in the app repository; "ADR" is `docs/adr/0016-nocturne-dark-only-and-tokens.md`,
"brief" is `docs/work/2026-11-16-nocturne/brief.md`, "design" is `design.md` in that folder, "A", "B", "C" are
`design-pr6-{a-onboarding,b-train,c-settings}.md`, "accent map" is `accent-map.md`):
`Ryft/Design/{Theme,Typography,RyftHeader,GlowTile,NeutralTile,Card,PillButton,CircleButton,ScreenHeader,
GroupedList,IconDisc,InlineLink,InsightCard,DotNumeral,RulerScale,DotRow,TickRow,OrbitGauge,RyftTabBar}.swift`,
`Shared/OrbitGaugePalette.swift`, and the shots in `docs/work/2026-11-16-nocturne/shots/` and
`docs/screenshots/` (read as images).

## 1. Principles

| # | Rule | Source | On the web |
| --- | --- | --- | --- |
| P1 | Dark-only; one value per colour; no appearance option. | ADR §1, §2; `Theme.swift` | `color-scheme: dark`, no light block, no switch, no script (web D20). A light system setting changes nothing: `e2e/visual.spec.ts` takes both and the two images are byte-identical. |
| P2 | The rings are the legend: one colour per metric, everywhere. Recovery teal, sleep violet, strain blue. | brief §1 "Farbregel"; `Theme.recovery`, `metricSleep`, `metricStrain` | Metric colours appear only where a metric is shown: the three glow tiles, the gauge, the score bands (sleep and strain ramps), the weight bars. |
| P3 | **Teal is recovery only (L27).** Links, buttons, focus, selection, checkmarks and icon tints are ink (`text-primary`), a spinner is `text-secondary`. | accent map, "Classes"; design §P5-1.1; A G1; C §2.3 | The site's old teal (eyebrow labels, links, step rules, the focus ring, the primary button, the selection) is gone. Teal remains on: the recovery tile, the readiness delta (recovery's colour with a triangle), the recovery weight bars and the readiness bands. |
| P4 | Volt (`signal`) is the one marked thing and never text: the ruler's needle, the readiness marker in the gauge, the "today" dot in a dot row, the active tab's dot, the hint dot on Insights. | brief §1 "Signal"; ADR §2.2; `Theme.signal` | The same five: ruler needle, gauge dot, the today dot in the hero's sleep tile, the current page's dot in the header nav, the hint dot on the Insights glow card. A review check (no contrast test can catch Volt as text). |
| P5 | Text: ink for titles and numbers, 62 % for labels and units, 48 % for scales and meta. **A raised fill (`surface-raised`) carries ink or 62 % text, never 48 %** (3.94:1 there). | ADR §2.4, §2.5; `Theme.swift` | `surface-raised` is used only as a bar track (no text). `tests/contrast.test.ts` checks every pair the site uses and states the rule. |
| P6 | One glow tile per screen section; everything else is a neutral tile on `surface`. | brief §1 "Glow"; design §P2-3 | The Today figure has three (the app's Today has three); the Insights sample has one. Cards elsewhere are neutral. |
| P7 | One hero number per screen, as a dot grid; the rest Light digits. | brief §1 "Hero-Zahl"; `DotNumeral.swift` | The dot numeral is the centre of the hero's gauge ("84"). Other numbers are Light type. |
| P8 | State is never colour alone: needle position, band word, shapes (filled / outlined / hollow), the VoiceOver sentence. | brief §6; `RulerScale.swift`, `DotRow.swift`, `InsightCard.swift` | Same: a delta carries a triangle, a set that is done is a filled disc with a check, an insight carries its word and a dot shape. Decorative instruments are `aria-hidden`, the text beside them says the value. |
| P9 | Sample data says so. | web D9; `CHART_STYLE.md` | Every drawn figure has a "Sample data" note. |

## 2. Tokens and numbers

Generated: colours (16 sets), `Theme.Metrics` (screen padding 20, section spacing 24, hairline 1, neutral tile
radius 22, glow 24, hero 28, circle button 40, hit target 44, pill 52, item spacing 12, control outline 1.5,
row 46), `CardSpec.padding` (16), the type scale with the label's case and tracking, the wordmark tracking,
motion, and the glow tile's ellipse and stops (`GlowTileSpec`, the `panel` kind: ellipse 90 % × 110 % at
50 % / 55 %, overlay `background` at 97 / 92 / 55 / 10 % at 0 / 34 / 68 / 100 %). `scripts/tokens.mjs` reads
`Theme.swift`, `Card.swift`, `GlowTile.swift`, `Typography.swift`, `RyftHeader.swift`; `tokens:check` in CI
fails on drift.

Component opacities live in `src/styles/nocturne.css`, each a token colour at the app's constant, and
`tests/nocturne.test.ts` reads that constant from the Swift: circle and icon ring 16 % (`CircleButton`),
pill outline 20 % (`PillButton`), link underline 50 % (`InlineLink`), pressed fill 8 %, chevron and arrow
out 35 % (`GroupedListSpec.accessoryOpacity`), dot numeral off 10 %, tick outside the zone 22 %, tick unlit
12 %, gauge tracks 16 / 7 / 13 % and the halo 16 % (`OrbitGaugePalette`), missed dot 50 %, no-data dot 35 %,
glow label 78 %, the flat glow border 2. It also checks the dot numeral's glyph table row for row.

`card-radius`, `tile-radius` (18, 16) are gone from the tokens: the app's single card radius is 22.

## 3. Type

| Rule | Source | On the web |
| --- | --- | --- |
| Titles and numbers Light (300), text Regular, buttons and emphasised row titles Medium. Never Bold. | `Typography.swift`; ADR §3 | `h1` display 52, `h2` title 26, `h3` headline 17 Medium, body 16, callout 15, label 13, caption 12, axis 11. Three font files (Light, Regular, Medium) against the LCP budget. |
| Labels are sentence case, no tracking, `text-secondary`. | `ryftLabelStyle`; ADR §3 | `--label-transform` and `--tracking-label` from the tokens, never hard-coded. |
| The wordmark: "RYFT" Light, 0.32 em, the trailing tracking pulled back. | `RyftHeader.swift` `Wordmark`; design D-h | `Wordmark`; `margin-right: -0.32em`. |
| Tabular digits everywhere. | `Font.ryft` | `font-variant-numeric: tabular-nums` on `body`. |
| A number under 20 pt is Regular (`lightFloor`). | `RyftFonts.lightFloor`; design D-f | Nothing on the site sets a Light number under 20 px. |
| Tab labels are 11 pt. | `RyftTextStyle.tab` | **Not used.** The header's nav is 15 px (callout): 11 px is a thumb-distance size on a phone, and a mouse reads a website from further away. `--type-tab` is generated and unused. |

## 4. Components

| App component | Source | On the web |
| --- | --- | --- |
| **Header lock-up**: the orbit mark (26) and the wordmark (15), 10 apart. Flat on the background, a hairline under it. | `WordmarkLockup`; `ScreenHeader`; brief §1 | `Logo` (the mark scales the wordmark with it). The header is sticky and flat: the old `backdrop-filter` glass is gone ("Keine schwebende Glas-Leiste"). |
| **Dot tab bar**: a 6 pt dot over a label; the active dot is Volt, the others ink at about 30 %. | `RyftTabBar`; design §P5-2 | The header's nav: `aria-current="page"` gets the Volt dot. It is a top bar, not pinned at the bottom: a pinned bar over a document hides content and focus, and the app's reason for it (a thumb) is a phone's. |
| **BrandLockup**: mark 44, wordmark 32, tagline as a label. | `RyftHeader.swift` | `Lockup`, in the footer. The tagline is the site's ("Your whole health."), not the app's launch line. |
| **Neutral card**: `surface`, radius 22, 16 inside, no border, no shadow. | `Card.swift`; `Theme.Metrics.neutralTileRadius` | `Card`. |
| **Neutral tile**: label above, a Light 24 value below, 80 high. | `NeutralTile.swift` | `Tile`, in the styleguide (no page needs one). |
| **Glow tile**: the metric colour under a radial overlay of `background`; text in the dark core; label 78 % (full in the recovery tile); radius 24 (28 for hero tiles); flat `surface` with a 2 px metric border under Reduce Transparency. | `GlowTile.swift`; brief §2, §6; design §P2-3 | `GlowTile` + `GlowLabel`. The gradient is CSS, from the `--glow-*` tokens; `@media (prefers-reduced-transparency: reduce)` draws the flat look. Every tile uses the `panel` ellipse (the app falls back to it on a wide tile). |
| **Dot numeral**: 5 × 7 cells per digit, dots 5 apart 7, off 10 %; fixed size, a graphic. | `DotNumeral.swift`; design §P2-1 | `DotNumeral` and the gauge's centre, drawn as two SVG paths from `src/lib/dots.ts`. The app's Light fallback (AX sizes, thin-digit switch) is not needed: the grid is a graphic, as in the app, and the page text carries the value. |
| **Ruler**: 61 / 64 / 43 ticks, ticks outside the zone 1 × 8 at 22 %, in the zone 1.5 × 14 tinted (ink 55 %, sleep 85 %, strain 95 %); a Volt needle; labels in 11 pt tertiary (the compact ruler in a glow tile: the tile's label ink). | `RulerScale.swift`; design §P2-2 | `Ruler`, SVG lines at percentage positions. Decorative (`aria-hidden`): the value is in the text next to it. |
| **Day dots**: 6 pt, 4 apart; full, missed (outline), today (Volt), no data (2 pt). | `DotRow.swift` | `DotRow`, in the hero's sleep tile. |
| **Orbit gauge v3**: strain as 128 ticks under a 3.5 arc, recovery and sleep on one middle ring split at the vertical (±6°, 160°), readiness as 60 ticks with a Volt dot, orbit dots with a 3 px ring and a 16 % halo; the dot numeral in the centre. | `OrbitGauge.swift`; design §P3-1 | `OrbitGauge`, drawn at 260 and scaled; the app's tiers below 150 pt are not needed. The strain dot and Volt dot cut the ticks they cover, as in the app. |
| **Pill button**: 52 high, round; primary is an ink fill with a dark 40 pt circle and a glyph; secondary an outline at 20 %; a link is ink underlined at 50 %. | `PillButton.swift`; `InlineLink.swift`; accent map | `ButtonLink` (`primary`, `secondary`, `quiet`) and the global `a` style. The skip link is the primary pill. The destructive, busy, compact styles and the Apple button have nothing to do here. |
| **Grouped list**: an optional 13 pt tertiary title, a `surface` card of rows (46 high, 16 at the sides) with a hairline inset 16 between them, a chevron or an arrow out at 35 %, a 12 pt footer. | `GroupedList.swift`; C §2.2 | `Group`, `GroupRow`, `GroupLink`: the footer's links, Science's table of contents, the home page's principles and FAQ, the licences list, tables in long text. |
| **Icon disc**: 36, no fill, a ring at 16 %, a thin ink glyph. | `IconDisc.swift` | `IconDisc`, on the "How it works" steps. |
| **Insight card**: `surface`, radius 24, a 6 pt severity dot and its word, a title, a body; the colour is on the dot only; the tile is the same rows in the recovery glow with the Volt hint dot. | `InsightCard.swift`; design §P5-5.1 | `InsightCard`, on the home page. |
| **Sparkline / HRV wave** in the recovery tile. | `TodayView`; `shots/pr4-today.png` | `Sparkline`. |
| **Motion**: ease-out, 250 ms, no springs, nothing counts up; Reduce Motion turns it off. | `Theme.Motion` | Hover colour changes and the FAQ's plus use `--motion-quick`; `prefers-reduced-motion` stops them (globals.css). Smooth scrolling only without a preference. |

**Left out, with the reason**: circle buttons, segmented pickers, steppers, toggles, text fields and every
form control (the site has no forms: web rule); the launch sequence, the entrance animation and the splash
(brief §5, `LaunchStage`: a script, and no cold start); the tab bar pinned to the bottom (above); Dynamic
Type tiers and Large Content Viewer (the browser's own zoom does it; sizes are rem); the pinned action bar
(C §2.1: no page has one primary action pending); coach marks; the widget; the app icon (already M1, web
D22; the tab icon stays the orbit mark, web D23).

## 5. Pages

| Page | Nocturne |
| --- | --- |
| `/` | Hero: display title, two pills, neutral dots; Today drawn natively (title, state, gauge, delta, ruler, glow tiles for recovery, sleep and strain) as a sample. How it works: three neutral cards with icon discs. Scores: four neutral cards, the legend's colours on bands and bars. Beyond the scores: a drawn rest timer and set list, the journal card, the insights cards (one glow). Principles and FAQ: grouped lists. Privacy facts: neutral cards. A closing card. |
| `/science` | A table of contents as a grouped list; long text in prose (below). |
| `/beta` | Display title, pills, ring bullets in ink, four numbered neutral cards. |
| `/support`, `/privacy`, `/terms`, `/impressum` | Long text in prose: Regular 16 in ink, Light titles, links ink with the underline, tables as grouped cards, a draft notice as an amber-bordered notice card. **The words are the legal texts' and are not touched here.** Support's steps are two neutral cards. |
| `/licenses` | A grouped list of expandable rows. |
| `/open`, 404 | The lock-up, the title, the pills. |
| `/styleguide` | Every component above, with the generated colours and type. |
| `/admin` | Unchanged apart from the tokens: its legacy names (`accent`, `on-accent`, `mist`, the two radii) map to `recovery`, `background`, `text-primary` and the neutral radius. |

The home page no longer shows the app's screenshots. The captures in `public/screenshots/` are of the
app before PR 4 (teal, old layouts), and the app's current captures can't replace them through
`npm run screenshots` (it wants light captures and a Train session the app no longer writes). The pages draw
what they show, in the app's own components.

## 6. Accessibility

WCAG 2.2 AA. The focus ring is a 2 px ink outline. Controls are at least 44 px high (pills 52). Links are
underlined. Instruments are `aria-hidden` where the text next to them says the value; the gauge is an image
with the readiness as its label. `e2e/site.spec.ts` runs axe on every page under both settings;
`tests/contrast.test.ts` pins the pairs.

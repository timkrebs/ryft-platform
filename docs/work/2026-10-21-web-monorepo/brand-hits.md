# The denylisted brand outside `web/` (2026-10-21)

`web/config/brand-denylist.json` over every tracked file of this repository outside `web/` and over every
commit message (masked ▇, so this file never repeats it). The check in CI and the hooks covers `web/**`, the
built site and new commit messages only; these are internal, so the user decides (brief §2).

**Hard (6), proposed wording:**

| Where | Now (masked) | Proposed |
| --- | --- | --- |
| `README.md:3` — the repository's front page | Your own ▇▇▇▇▇: Garmin watch → Apple Health → iOS app → Go microservices on Kubernetes → daily Recovery / Strain / Sleep scores. | Daily Recovery, Strain and Sleep scores from any watch or ring that writes to Apple Health: Apple Health → iOS app → Go microservices on Kubernetes. |
| `docs/ARCHITECTURE.md:3` | A self-hosted ▇▇▇▇▇: a Garmin watch feeds Apple Health, … | RYFT, self-hosted: a watch or ring (a Garmin, in development) feeds Apple Health, … |
| `docs/ARCHITECTURE.md:168` | …the morning's recovery and the day's strain line up like ▇▇▇▇▇. | …the morning's recovery and the day's strain line up. |
| `backend/internal/scoring/algorithms.go:1` (comment) | Package scoring implements the ▇▇▇▇▇-style Recovery / Strain / Sleep model … | Package scoring implements RYFT's Recovery / Strain / Sleep model … |
| `backend/internal/scoring/algorithms.go:248` (comment) | …a day with walking and stairs 3-6, like ▇▇▇▇▇. | …a day with walking and stairs 3-6. |
| `docs/DECISIONS.md:41` (D5) | …shows quality 82 as "good" — so ▇▇▇▇▇'s 85/70 split cannot be used. | …shows quality 82 as "good" — so an 85/70 split cannot be used. |

**Warnings (2):** `docs/DECISIONS.md:151` "the alternative to `secrets.yaml`" (comparison rule; a false positive, no
change); `docs/DECISIONS.md:817` the milestone name in "M3 (Strain Coach: …)" (borrowed-feature-name; proposed
"M3 (strain target: …)", and the same in `../ROADMAP.md` if you rename the milestone).

**Commit messages:** none of this repository's 165 commits mentions it. Git history keeps the old file
contents; rewriting history is not proposed (no force-push).

The app's hits (`web/docs/BRAND_CHECK.md`) are for the app session.

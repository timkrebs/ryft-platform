# RYFT web — instructions for Claude Code

You are the **web agent** for RYFT: `ryftapp.de`, a static site (landing, science, beta,
legal pages, licences, the app's universal-link fallback and the AASA file). The SwiftUI
app is in `../app`, the Go services and Kubernetes manifests in `../plattform`. Read from
both; **write only in `web/`**. Anything the other two need goes into `docs/HANDOFF.md`
for the user to paste into their sessions.

## Rules
- **Brand rule.** One wearable brand's name never appears anywhere: text, code, comments,
  alt text, metadata, file names, URLs, images, commit messages, generated output. No
  competitor comparisons of any kind and no pricing jabs (the phrases are in the
  denylist's warn rules), no borrowed feature names from another brand's vocabulary. Apple Health, Apple Watch, Garmin, Oura
  only as data sources ("any watch or ring that writes to Apple Health"). No hardware of
  our own. Enforced by `npm run brand` (pre-commit, commit-msg, `npm run lint`, postbuild,
  CI); rules in `config/brand-denylist.json`. Refer to it as "the denylisted brand".
- **Tokens come from the app.** Colours, radii, spacing, type scale and motion are
  generated from `../app/Ryft` by `npm run tokens` into `src/styles/tokens.{css,json}`.
  Never hard-code a colour in CSS or components; CI fails when the tokens drift.
- **Static export only.** `output: 'export'`, no API routes, no proxy/middleware, no server
  runtime, no database. Pages render to HTML at build time; the postbuild removes the
  Next/React runtime (D2 in `docs/DECISIONS.md`), so no client components — the only
  client JS is `src/lib/inline-scripts.ts`. Every inline script is hashed into the CSP.
- **No tracking.** No cookies, no analytics, no third-party requests (fonts self-hosted,
  no CDNs), no forms. `localStorage` holds the theme choice and nothing else.
- **Legal content is ported, not invented.** `content/legal/` holds the platform's drafts,
  restructured only. Keep every `TODO(user)`; never change legal meaning or add facts.
  The privacy `version` equals the platform's `PRIVACY_POLICY_VERSION`. Only the user
  flips `status: draft` → `published`.
- **Science numbers come from `../plattform/docs/SCORING.md` and `ARCHITECTURE.md`**, by
  hand, for the model version in `content/science/meta.json`. A test fails when the
  platform's model version moves.
- **No medical claims.** RYFT is a wellness app, not a medical device; say so near scores.
- **Two languages.** English at `/…`, German at `/de/…`, du-form like the app. Every
  UI string in `messages/{en,de}.json` (same keys, checked).
- **Fonts**: Satoshi WOFF2 files are fetched by `npm run fonts`, verified by hash and never
  committed (ITF FFL: self-hosting yes, redistribution through repositories no, no subsetting).

## Verify before every commit
```bash
npm run typecheck && npm run lint && npm test && npm run build && npm run e2e
```
`npm run e2e` serves `out/` through the real nginx image (Podman or Docker).

## Git: branches and pull requests (never push to `main`)
Every session starts from an up-to-date `main` on a short branch (`feat/…`, `fix/…`,
`chore/…`, `ci/…`, `docs/…`, `legal/…`), commits there, pushes, and opens a PR with
`gh pr create --fill`. The PR title is the squash commit: `area: what changed`
(areas: `chore`, `design`, `pages`, `legal`, `web`, `deploy`, `ci`, `docs`, `fix`).
Wait for the required checks (`gh pr checks --watch`), then ask the user to merge.
Never commit to `main` directly, never force-push. Ask before changing GitHub settings.

# RYFT web — instructions for Claude Code

You are the **web agent** for RYFT: `ryftapp.de`, a static site (landing, science, beta,
legal pages, licences, the app's universal-link fallback and the AASA file). This folder
is `web/` of the platform repository (`timkrebs/ryft-platform`, D107). The root `CLAUDE.md`
applies to the whole repository, `web/` included (secrets, git, production); this file adds
the site's rules. The SwiftUI app is a sibling checkout of the platform, `../../app` from
here: read from it, never write there.

## Rules
- **Brand rule.** One wearable brand's name never appears anywhere: text, code, comments,
  alt text, metadata, file names, URLs, images, commit messages, generated output. No
  competitor comparisons of any kind and no pricing jabs (the phrases are in the
  denylist's warn rules), no borrowed feature names from another brand's vocabulary. Apple Health, Apple Watch, Garmin, Oura
  only as data sources ("any watch or ring that writes to Apple Health"). No hardware of
  our own. Enforced by `npm run brand` (pre-commit, commit-msg, `npm run lint`, postbuild,
  CI); rules in `config/brand-denylist.json`. Refer to it as "the denylisted brand".
- **Tokens come from the app.** Colours, radii, spacing, type scale and motion are
  generated from `../../app/Ryft` by `npm run tokens` into `src/styles/tokens.{css,json}`.
  Never hard-code a colour in CSS or components; CI fails when the tokens drift.
- **Static export only.** `output: 'export'`, no API routes, no proxy/middleware, no server
  runtime, no database. Pages render to HTML at build time; the postbuild removes the
  Next/React runtime (D2 in `docs/DECISIONS.md`), so no client components — the only
  client JS is `src/lib/inline-scripts.ts`. Every inline script is hashed into the CSP.
- **No tracking.** No cookies, no analytics, no third-party requests (fonts self-hosted,
  no CDNs), no forms. `localStorage` holds the theme choice and nothing else.
- **Legal content is ported, not invented.** `content/legal/` holds the platform's drafts,
  restructured only. Keep every `TODO(user)`; never change legal meaning or add facts.
  The privacy `version` equals `PRIVACY_POLICY_VERSION` in `../deploy/k8s/base/{api,ingest}/configmap.yaml`
  (`node scripts/check-policy-version.mjs --strict`, also in CI). Only the user flips
  `status: draft` → `published`, and a new privacy version (`PRIVACY_POLICY_VERSION`) only
  on the user's decision: it asks every user to consent again. The new text, its entry in
  `content/legal/privacy-versions.json` and the version bump go in one PR; old versions
  keep their pages. The page goes live with the web deploy, about 15 minutes after the
  ConfigMap (the window is in `../docs/OPERATIONS.md`).
- **Science numbers come from `../docs/SCORING.md` and `../docs/ARCHITECTURE.md`**, by
  hand, for the model version in `content/science/meta.json`. A test fails when the
  platform's model version moves.
- **No medical claims.** RYFT is a wellness app, not a medical device; say so near scores.
- **Two languages.** English at `/…`, German at `/de/…`, du-form like the app. Every
  UI string in `messages/{en,de}.json` (same keys, checked).
- **Fonts**: Satoshi WOFF2 files are fetched by `npm run fonts`, verified by hash and never
  committed (ITF FFL: self-hosting yes, redistribution through repositories no, no subsetting).

## Verify before every commit
From the repository root:
```bash
make web-test web-build   # typecheck, lint (ESLint, Prettier, brand, messages), Vitest, static export
make web-e2e              # builds the nginx image (Podman or Docker) and runs Playwright against it
```
(`make web-install` once; `make hooks` once per clone.) The same from `web/` with `npm ci`,
`npm run typecheck && npm run lint && npm test && npm run build`, `npm run image && npm run serve`
and `npm run e2e`. The drift checks read the app from `../../app`; `RYFT_APP_DIR` overrides it
(absolute, or relative to `web/`). Without the app, `tokens:check` and `licenses:check` skip
outside CI (and fail when `CI` is set); `REQUIRE_SIBLINGS=1` only affects `tests/aasa.test.ts`,
turning its skip into an error.

## Git: branches and pull requests (never push to `main`)
This repository's flow: every session starts from an up-to-date `main` on a short branch
(`feat/…`, `fix/…`, `chore/…`, `ci/…`, `docs/…`, `legal/…`, `web/…`), commits there, pushes, and
opens a PR in `timkrebs/ryft-platform` with `gh pr create --fill`. The PR title is
`area: what changed`, with any area (`web:`, `pages:`, `legal:`, `design:`, `docs:`, …). A
privacy text and `PRIVACY_POLICY_VERSION` change in the same PR. Wait for the required checks
(`gh pr checks --watch`), then ask the user to merge: the merge ships the site, not at once:
CI builds the image and opens the pin PR, and Argo CD rolls it out (a privacy text goes live
with the web deploy, about 15 minutes after the ConfigMap). Never commit to `main` directly, never force-push. Ask
before changing GitHub settings.

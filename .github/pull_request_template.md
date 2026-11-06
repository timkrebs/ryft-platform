## What and why

## Checks
- [ ] Title reads `area: what changed` (any area: `web`, `deploy`, `infra/vault`, `events` …)
- [ ] Tests for the change, `make k8s-validate` for manifests, `docs/` updated; a judgement call has a `D<n>` in `docs/DECISIONS.md`
- [ ] `docs/API.md` changed? Additive only, with a JSON example and a changelog entry

- [ ] Changes to `.github/`, `scripts/`, `.gitleaks.toml`, `web/config/` or `web/scripts/check-*` read line by line (a PR runs its own CI)

### If `web/` changed
- [ ] Copy in English, in `web/messages/en.json` and `web/content/` (the site is English only, web D18)
- [ ] Colours only from `tokens.css`; no JavaScript on the marketing pages (web D2, D19: `script-src 'none'`)
- [ ] Legal text changed? Only the user publishes it; `portedFrom` removed if edited. A new privacy version goes with `PRIVACY_POLICY_VERSION` in `deploy/k8s/base/{api,ingest}/config.env` in the same PR (the strict check in `manifests`); it goes into force on EKS with the web deploy (promote moves the pin)
- [ ] Screenshots of changed pages, light (the only appearance, web D19; the CI Playwright report has all of them)

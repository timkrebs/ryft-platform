## What and why

## Checks
- [ ] Title reads `area: what changed` (any area: `web`, `deploy`, `infra/vault`, `events` …)
- [ ] Tests for the change, `make k8s-validate` for manifests, `docs/` updated; a judgement call has a `D<n>` in `docs/DECISIONS.md`
- [ ] `docs/API.md` changed? Additive only, with a JSON example and a changelog entry

- [ ] Changes to `.github/`, `scripts/`, `.gitleaks.toml`, `web/config/` or `web/scripts/check-*` read line by line (a PR runs its own CI)

### If `web/` changed
- [ ] Copy in English, in `web/messages/en.json` and `web/content/` (the site is English only, web D18)
- [ ] Colours only from `tokens.css`; no new client JavaScript (D2)
- [ ] Legal text changed? Only the user publishes it; `portedFrom` removed if edited. A new privacy version goes with `PRIVACY_POLICY_VERSION` in `deploy/run/config.env` (Cloud Run, where deploy.yml ships it with the merge) and its copies in `deploy/k8s/base/{api,ingest}/config.env` (equal to it) in the same PR (the strict check and `check_policy_in_force.sh` in `manifests`); the merge's `deploy.yml` run publishes the page with the site (its `site` job, once `SITE_DEPLOY` is on)
- [ ] Screenshots of changed pages, dark and light (the CI Playwright report has all of them)

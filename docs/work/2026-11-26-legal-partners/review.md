# Review — PR #252 (code-reviewer, one pass, 2026-11-26)

| # | Sev | Where | Finding | Fix | State |
|---|---|---|---|---|---|
| B1 | blocker | support.mdx:12, terms:22, privacy:21 | No Google sign-in in either repo; app ea52758 still offers email/password (`LogInView.swift:32-43`, `AuthService.swift:93-113`); "no password of its own" false for older email accounts; support is unversioned and would publish it | TODO(user) in support and terms; privacy §3 names older email accounts | fixed |
| S2 | should | privacy:41 | Consent gate drops provider deregistration notices (`store/inbox.go:139-140`, `cmd/ingest/connect.go:305-308`, `store/direct.go:320-324`) | qualify text; code fix before phase 2 (open item for backend) | text fixed; code open |
| S3 | should | privacy:40 | Failed inbox rows keep payload; purged 7 d after failing → up to 14 d (`inbox.go:314,355,366`); the 30-day record holds record_id, kind, timestamps (`0042_connections.sql:210-226`) | "14 days"; reword record | fixed |
| S4 | should | privacy:76 | 90-day listing only for provider-side ends; in-app disconnect deletes the row (`store/connections.go:525-527`, `direct.go:358`) | reword | fixed |
| S5 | should | messages/en.json :298 iOS 17, :279/:285/:316 invite codes vs BETA_GATE=off (D241), :396 "Change password" alt | fix in this PR | fixed (see commit) |
| S6 | should | config.env:65 | CONNECT_MIN_POLICY_VERSION stays 2026-10-26 against D259's rule | left for the user (U14), recorded in D290 and the PR | open (user) |
| N1 | nit | privacy:41 | retry queue also keeps account id and provider user id (`connections.go:516-519`) | reword | fixed |
| N2 | nit | privacy:73 | backups 7 days has no facts row (console item) | facts row + note | fixed |
| N3 | nit | privacy App Attest | no installation id stored; key is (user_id, key_id) (`0037_app_attest.sql:44`) | "per account and key" | fixed |
| N4 | nit | support.mdx:32 | "Withdraw consent" row appears only when consent is current (`ProfileRows.swift:119-120`) | mention "Review" state | fixed |
| N5 | nit | terms:34 | disconnect/delete sentence reads as covering Apple Health and Google | limit to Garmin/Suunto | fixed |

# Review — PR #251 (code-reviewer, one pass, 2026-11-26)

Nothing blocks. Legal-page screenshots checked: no readable personal details.

| # | Sev | Where | Finding | Fix | State |
|---|---|---|---|---|---|
| A1 | should | web/src/components/insight.module.css:18 | glow card severity word text-secondary ≈ 4.1:1; app uses ink (`InsightTile.swift:44`) | `.glow .word { color: var(--text-primary) }` | fixed |
| A2 | should | instruments.module.css:35, :44-52; Home.module.css:351-367; ui.module.css:136 | opacities outside nocturne.css, untested against the app | follow-up: move into nocturne.css with test rows | open (follow-up) |
| A3 | should | e2e/visual.spec.ts:76 | mask chosen by pattern, nothing asserts it hit | follow-up: mask first section on /terms, /privacy*; assert ≥1 | open (follow-up) |
| A-n | nits | nocturne.test.ts:835/838/851; nocturne.css:20 unused var; Home.module.css:95 tracking; admin.css teal; P1 byte-identical untested | follow-up | open |
| A-dead | follow-up | Screenshot.tsx, src/data/screenshots.json, scripts/screenshots.mjs, public/screenshots (70 files deployed, unlinked) | follow-up PR | open |

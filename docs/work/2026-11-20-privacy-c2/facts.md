# C2: the source of every fact in `privacy-2026-11-20.mdx`

Checked on 2026-11-20 against `origin/main` `1832748`. The app was read in `ryft/app` (the working copy, "WC"), compared
with `ryft-app-main` (a main checkout of 2026-11-17, "MAIN"). *(search)* = from search results, not from a page read.
Console = the repository can't show it; it's on the user's list.

| § | Statement | Source |
| --- | --- | --- |
| 2 | Writes strength workouts: type, start, end; no heart rate, no energy | app `Ryft/Environment/HealthStore.swift:433-446` (WC); `Info.plist:81` |
| 2 | Free beta, TestFlight, invited testers only, no codes handed out | D241; the brief |
| 3 | Account fields | migrations `0001`, `0007`, `0017`, `0025`, `0028`; `users` |
| 3 | Sign in with Apple token stored encrypted (AES-256-GCM, key `data-key`, api only), revoked on deletion | D48, `infra/gcp/secrets.tf:18`, `deploy/run/api.yaml` (`DATA_ENCRYPTION_KEY`), `backend/internal/store/store.go:155-160` |
| 3 | Email and password sign-in at Supabase | app `Auth/AuthService.swift:91-106`; D159 |
| 3 | Health types | app `Ryft/Health/HealthKitManager.swift:50-73, 230-259` (WC = MAIN) |
| 3 | Apple Health record identifier (`hk_id`) | migration `0029` |
| 3 | Duplicate workout (`duplicate_of`) | migration `0031` |
| 3 | Device fields, sync summary, push `last_success_at`, `notifications_sent` | migrations `0015`, `0020`, `0023` |
| 3 | App Attest: key id, public key, counter, environment, attested/last used; challenges SHA-256, 5 min; no receipt, model, address; token 1 h, not stored | migration `0037`; D245, D247 → Privacy (`docs/DECISIONS.md` ≈6163-6177, 6356-6362); app `Networking/AppAttestSeams.swift`, `AppAttestor.swift` (WC only) |
| 3 | The device talks to Apple, the servers never call Apple | D247 → Privacy |
| 3 | Consent record | migration `0017` (`consents`); D78 |
| 3 | Analytics: opt-in, off by default, a fixed event list, pseudonymous, no IP | D231; `docs/API.md` → `GET /v1/config` → Analytics; the app's analytics plan (root repo, not readable here); **the app has no analytics code yet** (WC and MAIN) |
| 3 | Analytics: no health data | the brief (from the analytics plan); **to confirm against the plan** |
| 3 | Feedback fields and diagnostics, on by default, no health values | migration `0021`; app `Features/Profile/ReportProblemView.swift:240-297`, `ProfileViewModel.swift:1820` |
| 3 | Our log line: method, path, status, bytes, duration, request id, user id; no IP, email or body | `backend/internal/httpx/httpx.go:269-275`, `backend/internal/logx/logx.go:150-162`; `docs/OPERATIONS.md` → Data protection; D222 |
| 3 | Scoring no longer logs scores | `docs/OPERATIONS.md` → Data protection (≈4499-4509) |
| 3 | Google's logs: IP, URL, user agent of denied/flagged LB requests and api/ingest 5xx | D242 → Privacy; `infra/gcp/logging.tf:11-45` (exclusions `lb-requests-unflagged`, `run-requests`); `edge.tf:122-141` |
| 3 | Per-IP limit: keyed hash in memory, never logged; idle after 1 min (edge) / 10 min (admin), swept only every 1024 requests or when full | D240; `backend/internal/httpx/clientip.go:41-67`, `internal/app/iplimit.go:27`, `cmd/api/adminauth.go:193`, `internal/ratelimit/ratelimit.go:15, 68-80` |
| 3 | Saved workout carries the brand name and the session id | app `HealthStore.swift:440-444` |
| 3 | Diagnostics also: sync phase, queue, backlog; the error from Details | app `ReportProblemView.swift:285, 295` |
| 3 | `/admin` keeps the operator's session in `sessionStorage`, cleared on leaving | `web/CLAUDE.md`; web D16 |
| 5 | Cloud Trace API on; whether traces are stored and for how long | `infra/gcp/project.tf:13`; **console** |
| 5, 6 | Supabase Auth's sessions and audit log (IP, user agent) | Supabase standard schema, not in the repo; **console (SQL)** |
| 5 | Push texts: scores, session strain, exercise names, records | `backend/internal/notify/messages.go:88-136` |
| 5 | Events also carry a session id and a request id | `backend/internal/model/events.go:23-64` |
| 6 | Challenges deleted on the 15-minute tick | `infra/gcp/scheduler.tf:9`; `backend/internal/store/appattest.go:163` |
| 6 | Deleted ids stay in `feature_flags.allow_user_ids` | migration `0014`; `store.go:144-200` (`DeleteUser` doesn't touch it) |
| 8 | Admin detail: max HR and source, tz, identities with emails, last sign-in, counts, sources, sync summary, duplicates; no scores | `backend/internal/model/admin.go:12-131` |
| 3 | Cloud Armor's rate rule keyed on the IP | `infra/gcp/edge.tf:175` |
| 3 | Invite mail link on the beta page | `web/messages/en.json:333-335`, `web/src/…/Beta.tsx` |
| 3, 6 | Bound invite email and note, kept until 30 days after the code stops being redeemable, codes at most 90 days | D159, D161; PR #129's body |
| 3 | Sign-in mails from Supabase | D159 (Confirm email on), D239 |
| 3 | Site: no access log, no cookies, no browser storage, no analytics, no third parties | web D19 (no logging keys in `firebase.json`), web D20, `web/CLAUDE.md`; **Hosting's Cloud Logging integration: console** |
| 5 | Google Cloud, europe-west1, the products | `infra/gcp/variables.tf:12-15`, `project.tf:7-36`, `deploy/run/*.yaml`; D216, D217, D218, D219, D222 |
| 5 | Events carry user id and days, no scores | `backend/internal/model/events.go:13-64` |
| 5 | Scheduler: one tick, payload `{}` | `infra/gcp/scheduler.tf:4-19` |
| 5 | Monitoring: counters without user data; notifications carry no line fields; email and Cloud app channels | `infra/gcp/monitoring.tf:41-42`, `alerts.tf:8-23, 204-252`; D236, D237, D244 |
| 5 | Load balancer and Cloud Armor are global | `infra/gcp/edge.tf:70-77, 265-325`; `edge = "live"` in `gates.auto.tfvars:16` |
| 5 | Google LLC DPF, CDPA with SCCs | *(search)*: cloud.google.com/terms/alternative-transfer-solution |
| 5 | Firebase Hosting, project `ryftlab-site` | D233; web D19 |
| 5 | Supabase Frankfurt, DPA Supabase Pte. Ltd. | `deploy/run/config.env` (`SUPABASE_POOLER_HOST`), `docs/SUPABASE.md`; LEGAL_CHECKLIST 0a; *(search)* supabase.com/legal/dpa |
| 5 | Supabase's own logs hold IPs; 7 days on Pro | *(search)*; **console** |
| 5 | Push texts can carry scores | `backend/internal/notify/messages.go:88-136`; `docs/API.md` → Push |
| 5 | PostHog EU Cloud, Frankfurt; DPA with PostHog, Inc. | D231 (`eu.i.posthog.com`); *(search)* posthog.com/dpa, PostHog Cloud EU post |
| 5 | "Discard client IP data" on | `docs/OPERATIONS.md` → PostHog's project key (≈2805, 2854); **console** |
| 5 | Microsoft 365 through GoDaddy for support@; Supabase SMTP as no-reply@; Sent Items copy, 7 days, then 14 days recoverable | D214, D239; `docs/OPERATIONS.md` → GCP → 14a.4 step 5; **whether the route is live and the retention is set: console** |
| 5 | GoDaddy registrar only | `infra/gcp/dns.tf:3`; D213, D214 |
| 6 | Deletion covers every table incl. App Attest and feedback; Supabase user and Apple revocation after the commit, retried | `backend/internal/store/store.go:161-194`; D39; `docs/API.md` (≈798) |
| 6 | Tombstone and `admin_audit` keep the id | migrations `0007`, `0022`; D88 |
| 6 | Backups 7 days | `docs/OPERATIONS.md` → Backups; **PITR: console** |
| 6 | Logs 14 days, `ryft-logs`, europe-west1 | `infra/gcp/logging.tf:3-9`; **`_Default` disabled: console** |
| 6 | Pub/Sub 7 days, dead letters 14 days; outbox published rows 7 days | `infra/gcp/pubsub.auto.tfvars.json`; `backend/internal/events/outbox/outbox.go:23-33, 412-418` |
| 6 | App Attest keys: deletion, replacement (10 per account), 90 days unused | `backend/internal/store/appattest.go:162-168`; D247 |
| 7 | Export: account, days, workouts, sessions | D245 (≈6088); app `DataExport.swift` |
| 8 | One service account and DB role per service; Secret Manager in europe-west1 | D222; `infra/gcp/secrets.tf:47-53` |
| 8 | Admin: TOTP (aal2, ≤ 12 h) or `ADMIN_API_KEY`; every call audited; what it shows | `backend/cmd/api/adminauth.go:26-41`; `docs/API.md` → Admin (≈2316-2637); D88, D153 |
| 9 | Minimum age 16 | `backend/internal/scoring/maxhr.go:9`; app `OnboardingCoordinator.swift:510-519` |

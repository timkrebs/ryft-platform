# Legal checklist — before the legal pages are published

**Not legal advice.** This list says where each open point is, what it needs, and, where the
answer is a fact about the RYFT system, that fact and where it was checked. Legal conclusions
(bases, whether a DPO is required, which authority, liability, law) are yours and your
reviewer's. The drafts change only with your answers, in both languages at once: those of
2026-10-21 are in ryft-web PR #10 (section 0a); everything else is open.

As of 2026-10-21; checked against `ryft-platform` `003a1a9`, `ryft-app` `1eb5192` and the
web image built from this repository.

Files: `content/legal/{en,de}/privacy/2026-10-20.mdx`, `…/terms/2026-10-20.mdx`,
`…/support.mdx`, `…/impressum.mdx`. Line numbers are the same in English and German unless
noted. 45 `TODO(user)` in all: privacy 11 + 11, terms 5 + 5, support 2 + 2, Impressum 4
(English) + 5 (German).

---

## 0. Your personal details stay off the internet (decided 2026-10-21)

You don't want your name, home address, personal email or phone publicly available. So
**none of them goes into the texts, this repository, its variables or commits**, and the
markers that ask for them stay open: U1, U5, U6, U7, U12 below, and the Impressum.

They conflict with what the drafts are built to say. The privacy policy names the
controller's identity and contact (Art. 13(1)(a) GDPR); the German Impressum asks for a name
and an address that can be served (§ 5 DDG); terms §1 names the provider. Whether and how
those apply to a free, invite-only beta, and what satisfies them, is for your legal
reviewer. The usual ways to meet them without your private details, for that review:

| Spot | Instead of your private details |
| --- | --- |
| Every published email (support, privacy, Impressum, `SUPPORT_EMAIL`) | An address on `ryftapp.de` (e.g. `support@`, `privacy@`) that forwards to your inbox. This needs mail for the domain (MX records in Route 53 and a forwarding or mail service). That service then receives users' emails, so it goes into privacy §5 like the others. |
| Postal address (Impressum, privacy §1, terms §1) | A business or service address that accepts legal mail for you. Whether a given one counts as *ladungsfähig* is the reviewer's call. |
| Name | A company (e.g. a UG) as provider and controller, with its own registered address. A bigger step. |
| Phone (Impressum's "second fast way") | Whatever the reviewer accepts in its place. The site has no forms by design. |

Facts that bear on it:

- **Already public:** your full name, as the author of every commit in the public
  `ryft-app` and `ryft-platform` repositories and as your GitHub profile name. Your GitHub
  account name is in the app's bundle ID `dev.timkrebs.ryft`, so in the AASA on `ryftapp.de`
  today (the platform's ConfigMap serves it), in the image names and in both public
  repositories. A bundle ID can't change for a published app. TestFlight also shows testers
  the developer name of your Apple account. What can still stay private: the address, the
  personal email, the phone.
- **Git keeps what is committed.** An address pushed once stays in the history, even after
  it is edited out, so it goes in only once you have decided.
- **App Store, later:** Apple asks EU sellers to declare whether they are a trader under the
  Digital Services Act; for traders, the App Store page shows an address, phone and email.
  TestFlight doesn't ask.
- Until then the pages stay drafts (banner, `noindex`).

## 0a. Answered on 2026-10-21

| Point | Your answer | Where it goes |
| --- | --- | --- |
| Contact email (U1's email, U5, U6's email, U12) | `support@ryftapp.de`, forwarding to your inbox | PR #10; `SUPPORT_EMAIL` is set. The mail service is still to choose and set up (`docs/HANDOFF.md` → 1); the text names it in a new marker |
| U2 | No data protection officer | PR #10 |
| S1 | Supabase in Frankfurt, its DPA accepted | PR #10; the basis for access from outside the EU stays open |
| S3 | AWS's DPA accepted | PR #10 |
| S4 | RYFT's logs excluded from Grafana Cloud | PR #10 (metrics only). True once you have excluded `ryft-.*` in Fleet Management (`docs/HANDOFF.md` → 1) |
| S5 | Supabase Pro, point-in-time recovery off | PR #10 (backups 7 days; dead letters corrected to 14 days) |
| S6 | Access log without IP, user agent, referrer and query string | PR #9 (nginx), PR #10 (the text, plus a marker for its basis) |
| S7 | The Apple Health opt-in sentence now | PR #10 |
| S8 | Minimum age 13 | PR #10 (privacy §9, terms §4) |
| — | The beta is invite-only, no public TestFlight link | `TESTFLIGHT_URL` stays unset |

**Still open:** the name and postal address (U1, the Impressum, terms §1; section 0), the
supervisory authority (U4), the review of the legal bases (U3, now also the website's), the
basis for Supabase access from outside the EU, the mail service (name, where, DPA), the
Impressum's § 18(2) MStV person, phone replacement, VAT ID and VSBG statement (U6–U8),
terms §8–§10 (U9–U11), the feedback route on the support page (S9), whether the old Ireland
project is deleted (S1), an SMTP provider for Supabase's emails (S2), whether sent `outbox`
rows are deleted (S5), and the privacy version (section 4).

## 1. Only you can answer

| # | Where | What is needed |
| --- | --- | --- |
| U1 | privacy §1 (l. 13); terms §1 (l. 12); Impressum "Provider"/"Angaben gemäß § 5 DDG" (en l. 14, de l. 12) | Controller and provider: full name (or company), postal address that can be served (*ladungsfähige Anschrift*), email address. The same person in all three. |
| U2 | privacy §1 (l. 13) | **Answered (2026-10-21): none.** The text will say none is designated. Whether one is required (Art. 37 GDPR, § 38 BDSG) is your reviewer's to confirm. |
| U3 | privacy §4 (l. 38) | The legal review of the bases listed in §4. The draft names them as the system works; it does not settle them. |
| U4 | privacy §7 (l. 59) | The competent supervisory authority (it follows from where the controller is established, usually your federal state's data protection authority). |
| U5 | privacy §7 (l. 59) | The email address for privacy requests. |
| U6 | Impressum "Contact" (en l. 18, de l. 16) | Email, and a second fast way to reach you (e.g. phone). |
| U7 | Impressum § 18(2) MStV (en l. 22, de l. 20) | Name and address of the person responsible for the content. |
| U8 | Impressum, German only (de l. 22) | VAT identification number, if you have one; the statement on consumer dispute resolution (§ 36 VSBG), if it applies to you. |
| U9 | terms §8 (l. 40) | How the beta ends, and how long before that testers are told. |
| U10 | terms §9 (l. 44) | The liability clause, reviewed for German law. The draft's note stays true: liability for intent, gross negligence and injury to life, body or health cannot be excluded. |
| U11 | terms §10 (l. 48) | Governing law and place of jurisdiction. |
| U12 | support (l. 10, first marker) | The support address: one that isn't personal (section 0). The same value goes into the repository variable `SUPPORT_EMAIL` (`docs/HANDOFF.md` → 1), which the support page and the beta page's "Ask for an invite" use. The beta is invite-only, with no public TestFlight link (2026-10-21), so that mail link is how people ask. |

## 2. Facts about the system, and the decisions they need

### S1 — Supabase: where the database is (privacy §5, l. 42)

The draft says Frankfurt (`eu-central-1`). **That is right for the current project.** The
first project was created in `eu-west-1` (Ireland); the platform recreated it in Frankfurt
before the beta (D49).

- `plattform/docs/STATUS.md:522`: "Supabase in Frankfurt: done; the new database was
  migrated by the deploy."
- `plattform/docs/DECISIONS.md` D49, `plattform/docs/SUPABASE.md` → "Moving the project to
  Frankfurt".
- The pooler host the services use (`plattform/infra/vault/terraform.tfvars`, host only):
  `aws-1-eu-central-1.pooler.supabase.com`. A Supabase pooler serves only its own region.
- `terraform.tfvars.example` shows the same pattern (`aws-<n>-eu-central-1…`).

Needs you: **confirm the region in the Supabase dashboard** (Project Settings → General), and
whether the old Ireland project is deleted. D49's runbook step 6 (pause it, delete it after
a week) is not recorded as done. If it still exists and holds any real person's data, it is a
second location: delete it, or the text has to name it.

Still open in the same marker: the **DPA with Supabase** and the basis for any access from
outside the EU (accept it in the Supabase dashboard; `plattform/docs/STATUS.md` → Beta step 2
lists it as open).

### S2 — Supabase: who sends the sign-in emails (privacy §5)

Supabase sends confirmation and reset emails. `plattform/docs/SUPABASE.md:30-33` says to enter
an SMTP provider before inviting people, because the built-in mailer sends only a few an hour.
**If you configured one**, it receives email addresses and must be named in §5 (who, where,
DPA). Needs you: which, if any (Supabase → Authentication → Emails → SMTP Settings).

### S3 — AWS (privacy §5, l. 43)

Verified: the cluster (`ryft-prod-euc1`), its load balancer, the KMS key and the CloudWatch
log group are in `eu-central-1` (`plattform/infra/aws`, `infra/aws/kubernetes/logs.tf`). The
DNS zone is Amazon Route 53, a global service with no region; it holds DNS records, no
personal data of users. The draft lists "DNS" under Frankfurt; your reviewer may want it
worded "… load balancer, in Frankfurt; DNS through Amazon Route 53".

Needs you: the **DPA with AWS** (AWS Artifact → Agreements).

### S4 — Grafana Cloud: do logs leave the cluster? (privacy §5, l. 45; §6, l. 53)

- **Metrics:** yes, always. The collector (`plattform/deploy/monitoring/k8s-monitoring.yaml`)
  sends cluster and service metrics to a Grafana Cloud stack in **eu-west-2 (London)**
  (`prometheus-prod-65-prod-eu-west-2.grafana.net`, l. 80). The services' metrics are
  counters and route labels, no user data (`plattform/docs/OBSERVABILITY.md` → Collection).
- **Logs:** the platform says **yes, by your choice**: "Left as they are, by choice: RYFT's
  pod logs also reach Grafana Cloud (not excluded)" (`plattform/docs/STATUS.md:536-537`). The
  collector can read every container's log file; which logs it ships is set in Grafana
  Cloud's Fleet Management, not in the repository, so the repository cannot prove either
  way. Those logs carry user IDs and daily scores (health data, `OPERATIONS.md` → Data
  protection) and, for the website, visitors' IP addresses (S6).
- **The UK:** the European Commission renewed the UK adequacy decision under the GDPR on
  19 December 2025; it runs until 27 December 2031 ([Hunton](https://www.hunton.com/privacy-and-cybersecurity-law-blog/european-commission-renews-uk-data-adequacy-decisions),
  [Freeths](https://www.freeths.co.uk/insights-events/legal-articles/2026/european-commission-renews-uk-adequacy-decisions-until-27-december-2031/)).
- **Retention:** the stack's plan. The free tier keeps logs 14 days (`OPERATIONS.md` → Data
  protection); check under Billing.

Needs you, one of:

- **Exclude** namespaces `ryft-.*` from pod-log collection in Fleet Management, then check
  that `{namespace=~"ryft-.*"}` in Explore returns nothing (what D51 originally intended).
  §5 then says "service metrics without personal data" only, and the §6 marker goes.
- **Keep** the logs there: the DPA with Grafana Labs, the transfer basis (the adequacy
  decision above), and the retention for §6.

### S5 — Backups and retention (privacy §6, l. 52–54)

- **Backups (l. 52):** "Supabase Pro: done (2026-10-20): daily backups, kept 7 days; PITR
  optional" (`plattform/docs/STATUS.md:456`; `OPERATIONS.md` → Backups). The draft's
  "kept for 7 days" matches. Needs you: confirm in the dashboard (Database → Backups) that
  the plan is Pro and **point-in-time recovery is off**. With PITR the retention is the
  add-on's, and the sentence changes.
- **Logs (l. 53):** 14 days in CloudWatch (`infra/aws/kubernetes/logs.tf:57`,
  `retention_in_days = 14`), plus Grafana Cloud's retention if logs go there (S4). Pod stdout
  also stays on the node until the pod goes.
- **Correction (l. 54):** the draft says events that could not be processed stay "up to
  30 days". Since the move to RabbitMQ (D102) dead letters are kept **14 days**: the `dlq`
  policy's `message-ttl: 1209600000` (ms) in `plattform/deploy/rabbitmq/topology/topology.yaml`,
  and `OPERATIONS.md:344` ("`dlq.scoring`, `dlq.notify`, 14 days"). The text needs 14, in both
  languages. Also: events waiting for the broker are rows of `outbox` in the database
  (D101) until they are sent; whether sent rows are deleted is not documented, so ask the
  platform session before publishing.

### S6 — Visitors of ryftapp.de are not covered (privacy, new text)

The privacy policy describes the app. The website itself logs every request: nginx's `main`
format in the image (`log_format main` in `/etc/nginx/nginx.conf`; `nginx/default.conf` turns
the access log off only for `/healthz`) writes the time, the request line **with its query
string**, status, bytes, referrer, user agent and `X-Forwarded-For`, which is the visitor's
IP address (Traefik forwards it; the load balancer preserves client IPs,
`infra/aws/kubernetes/addons.tf`). Fluent Bit ships every `ryft-*` namespace's container
logs, `ryft-web` included, to CloudWatch for 14 days (`logs.tf`), and to Grafana Cloud if S4
is "keep". Traefik's own access log is not enabled (`addons.tf`). No cookies, no analytics, no
third-party requests (this repository's rules, tested in `e2e/behaviour.spec.ts`).

Needs you, one of:

- **Recommended:** this repository logs without IP address, user agent, referrer and query
  string (time, method, path, status, bytes, duration). A small `fix:` PR here. The policy
  then needs one sentence that the IP address is used to deliver the page and for the
  per-IP rate limit, and is not stored.
- Keep the logs as they are; the policy gets a section on the website (what, why, 14 days,
  where), with its basis from your reviewer.

### S7 — Saving workouts to Apple Health (privacy §2 and §3)

The app is adding an opt-in "Save workouts to Apple Health"
(`app/CLAUDE_FOLLOWUP_BETA_FEEDBACK_1.md` §3): after a completed strength session it writes an
`HKWorkout` (traditional strength training: start, end, energy if known; no heart rate) to
Apple Health, only if the user turned it on. Not in `ryft-app` `main` yet. §2 says RYFT
"reads data from Apple Health", which stays true until it ships.

Needs you, one of: a sentence now, worded as opt-in (the app session confirms the wording,
`docs/HANDOFF.md` → 3), or a `TODO(user)` naming it until the feature ships.

### S8 — Minimum age (privacy §9, l. 67; terms §4, l. 24)

- The app and the platform accept birth years up to 13 years ago: `MinAge = 13` in
  `plattform/backend/internal/scoring/maxhr.go:9`, `latestBirthYear` `− 13` in
  `app/Ryft/Features/Onboarding/HealthPermissionView.swift:413`.
- Birth year is **optional** (it estimates maximum heart rate), so it is not an age check.
- The processing of health data rests on consent (§4). Art. 8 GDPR sets 16 as the age for a
  child's own consent to such services unless a member state lowers it; the draft's marker
  says Germany's is 16.

**Decided (2026-10-21): 13**, as the app and the platform accept today, in privacy §9 and
terms §4 (PR #10). The Art. 8 point above stays with your reviewer. No change is requested
from the app or the platform.

### S9 — Support page (support l. 10, second marker; l. 26)

- **The menu path for feedback:** the app has **no feedback screen** yet. `POST /v1/feedback`
  exists on the platform and in the app's `APIClient`, but nothing calls it. `app/docs/TESTFLIGHT.md:85`
  names "Profile → Report a problem", which doesn't exist (`docs/HANDOFF.md` → 3). What works
  today is TestFlight's own: take a screenshot, tap Share Beta Feedback
  (`app/docs/TESTFLIGHT_NOTES.md:22`); that feedback goes to Apple's App Store Connect, under
  Apple's terms (privacy §5 already names TestFlight). Needs you: TestFlight's route for
  now, or wait for the screen.
- Optional precision, l. 26: "Both are in the app's settings". They are Profile → Account →
  Delete account and Profile → Privacy → Withdraw consent (`ProfileView.swift:89`, `:313`).

## 3. Who receives personal data (privacy §5)

| Recipient | What for | Where | Personal data | Source |
| --- | --- | --- | --- | --- |
| **Supabase** | Database (Postgres) and sign-in | Frankfurt, `eu-central-1` (S1) | Everything in §3 | S1 |
| **Amazon Web Services** | The cluster that runs RYFT, its load balancer, logs (CloudWatch), keys (KMS) | Frankfurt, `eu-central-1`; DNS Route 53 (global, no user data) | Requests in transit, logs (user IDs, daily scores; website visitors' IPs, S6) | S3, S5, S6 |
| **Apple** | Sign in with Apple; Apple Push Notification service (push token and the notification text, which can carry scores, e.g. "Recovery 82 %"); TestFlight | Apple's | Sign-in identity, push token and texts, TestFlight feedback | privacy §5; `plattform/docs/OPERATIONS.md` → Push |
| **Grafana Labs** | Monitoring (Grafana Cloud) | London, `eu-west-2` | Metrics: none. Logs: as in S4 | S4 |
| **Your SMTP provider**, if any | Sign-in emails | ? | Email addresses | S2 |

Not recipients of users' personal data:

- **GitHub**: code and container images only.
- **HashiCorp** (HCP Vault, HCP Terraform): service credentials and Terraform state, no user
  data.
- **Let's Encrypt**: certificates for the domain.
- **Fontshare**: the fonts are fetched at build time; visitors never contact it.

## 4. The version

The privacy `version` is `2026-10-20`, equal to the platform's `PRIVACY_POLICY_VERSION`
(`npm run check:policy`: both ConfigMaps say `2026-10-20`). Users of the beta consented to that
version while it was a draft. Filling in the controller and the open points, and the
corrections above (14 days; the website; Apple Health; the age), change what they agreed
to. **Whether that needs a new version is your decision.** If yes, the text is published as
`content/legal/{en,de}/privacy/<new date>.mdx` (the old one stays at `/privacy/2026-10-20`),
and the platform sets `PRIVACY_POLICY_VERSION` to the same date in its api and ingest
ConfigMaps, so everyone is asked to consent again before the next upload.

## 5. Publishing (when every point above is answered)

1. Your answers into both languages; every `TODO(user)` resolved; `portedFrom:` removed from
   each edited file (the word-for-word test against the platform's drafts then stops for it).
2. `status: published` and `effective: 'YYYY-MM-DD'` in all eight files together. The draft
   banner and `noindex` go, the sitemap lists the pages, and `/privacy/2026-10-20` stays
   reachable.
3. `npm run check:policy`, `npm run brand`, `npm test`, build, e2e; PR
   `legal: publish privacy, terms, support and Impressum`; merge on green; the new image goes
   out with the platform's next pin.

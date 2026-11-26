# Legal checklist: the legal pages as RYFT runs today

**Not legal advice.** This list says where each open point is, what it needs and, where the answer is a fact
about the RYFT system, that fact and where it was checked. Legal conclusions (bases, roles, whether a DPO is
required, which authority, liability, law, what a partner agreement means) are yours and your reviewer's. The
texts change only with your answers.

As of 2026-11-26; checked against platform `main` `4bca49d` and the app at `ea52758`. The source of every
sentence in the new texts is `docs/work/2026-11-26-legal-partners/facts.md`; the partner programs are in
`…/partners.md`. The previous version of this list (2026-10-21, the AWS era) is in git history.

Files (English only, web D18): `content/legal/en/privacy/{2026-10-20,2026-10-26,2026-11-26}.mdx`,
`…/terms/{2026-10-20,2026-11-26}.mdx`, `…/support.mdx`, `…/impressum.mdx`. The German privacy notice for the app
beta is a separate item with counsel and not part of this list.

**`TODO(user)` count: 28 in the content files** (privacy 2026-11-26: 22, terms 2026-11-26: 2, Impressum: 3,
support: 1; after the review of #252: Google sign-in in terms and support, the older email accounts and the
deregistration-under-withdrawn-consent gap in privacy), plus 1 in `messages/en.json` (`legal.contact.responseTodo`, the response time on `/support`):
**25 on the site.** The older pages have none.

---

## 0. Your personal details stay off the internet (decided 2026-10-21)

You don't want your name, home address, personal email or phone publicly available. So none of them goes into
the new texts, this repository's new files, variables or commits, and the spots that need them are
`TODO(user)`: the Impressum (provider, second contact, § 18(2) MStV), terms 2026-11-26 §1 and privacy 2026-11-26
§1.

- **What the spots need** (for your reviewer): the controller's identity and contact (Art. 13(1)(a) GDPR); a
  name and an address that can be served (§ 5(1) no. 1 DDG); a second fast way of contact besides email
  (§ 5(1) no. 2 DDG); the person responsible for the content (§ 18(2) MStV); the applicant's company details,
  which both partner programs ask for (partners.md G1, G4, S1, S2: Suunto speaks of companies and
  organisations only).
- **The usual ways** to meet them without private details: a business or service address that accepts legal
  mail; a company (e.g. a UG) as provider, controller and partner applicant; for the second contact, whatever
  the reviewer accepts in place of a private phone. The site has no forms by design.
- **Already published, and kept:** privacy 2026-10-26 §1 and terms 2026-10-20 §1 carry your name and home
  address, and the Impressum on `main` carries them and your phone. The two versioned pages stay as they are:
  they are the record of what people consented to (D290). The Impressum is a single page: this PR removes them
  from it. Git keeps the history either way.
- **App Store, later:** Apple asks EU sellers to declare trader status under the Digital Services Act; for
  traders the store shows an address, phone and email. TestFlight doesn't ask.

## 1. The setup the texts describe (facts, with sources)

| # | Fact | Source |
| --- | --- | --- |
| F1 | The services run on Google Cloud in europe-west1 (Cloud Run, Pub/Sub, Scheduler, Secret Manager, Logging, Monitoring, Cloud Trace API on, DNS), behind a global load balancer with Cloud Armor | `infra/gcp/variables.tf:15`, `project.tf:8-33`, `gates.auto.tfvars:16`; D216, D217, D242 |
| F2 | The site is a static export on Firebase Hosting (project `ryftlab-site`): no cookies, no analytics, no access log of RYFT's | D233; web D19, D20 |
| F3 | Supabase in Frankfurt holds the database and the sign-in | `deploy/run/config.env:76`; `docs/SUPABASE.md:11`; D49 |
| F4 | AWS, Vault, RabbitMQ and Grafana Cloud are gone | D246, D3, D236 |
| F5 | Sign-in is Sign in with Apple or Google. **Your setting of 2026-11-26** (email auth off); the app at ea52758 still has the email screens and no Google sign-in, the platform has no Google path | facts.md, rows 1-2 |
| F6 | Mail for `support@ryftlab.de` is Microsoft 365, subscribed through GoDaddy; no sign-in emails are sent any more | D214, D239; your setting |
| F7 | The food diary exists on the platform and is dark (`nutrition.diary_enabled` and `nutrition.enabled` at 0 %); the app has no nutrition screen. CGM, N2R and coaching are dark too and left to a later version | D265-D280; migrations 0043-0052; facts.md |
| F8 | The food catalog is USDA FoodData Central (Foundation, SR Legacy) and Open Food Facts; BLS is excluded pending review, so no Max Rubner-Institut attribution is used | `scripts/etl/run_etl.py:26`; `docs/architecture/nutrition/06-data-source-licensing.md:7-10` |
| F9 | Product analytics: PostHog EU Cloud, opt-in, six events, a random id, Profile → Privacy → Share usage analytics. The app cannot send yet (a placeholder sink) | D231; app ea52758 `Ryft/Analytics/*` |
| F10 | Report a problem: Profile → About → Report a problem; diagnostics on by default, no health values | app ea52758 `Ryft/Features/Profile/ReportProblemView.swift:313, 325-346` |
| F11 | Export: account, days, workouts, sessions, manual entries; no journal, nutrition or connections | app ea52758 `Ryft/Features/Profile/DataExport.swift:14-33` |
| F12 | Deletion: everything, the Supabase user, the Apple grant, the connections at the providers (30 days of retries) | `backend/internal/store/store.go:163-192`; `docs/API.md:940-965`; D249, D257 |
| F13 | Garmin and Suunto connections: a foundation against a fake provider only; off in production | `backend/internal/connect/doc.go:18-19`; `deploy/run/config.env:52, 56`; `docs/API.md:997` |
| F14 | Minimum age 16 (terms 2026-10-20 said 13) | `backend/internal/scoring/maxhr.go:9`; app ea52758 `OnboardingSteps.swift:311` |

## 2. Only you can answer (numbered; the marker in brackets)

| # | Where | What is needed |
| --- | --- | --- |
| U1 | Impressum "Provider", terms §1, privacy §1 | Provider and controller: name (or company) and an address for service, the same in all three (section 0) |
| U2 | privacy §1 | Whether a data protection officer is required (Art. 37 GDPR, § 38 BDSG); the text says none is designated |
| U3 | privacy §5 | The review of every legal basis listed (the text proposes them) |
| U4 | privacy §8 | The competent supervisory authority (follows from U1) |
| U5 | Impressum "Contact" | A second fast way of contact that is not your phone |
| U6 | Impressum § 18(2) MStV | The person responsible for the content |
| U7 | privacy §3, §6 (two markers) | **Google sign-in:** set it up in Supabase and ship it in the app before this version goes into force; then confirm what Google shares and Google's role. Until then the text promises a sign-in that doesn't exist, and email accounts made before 2026-11-26 can't sign in (support says nothing about them: your call) |
| U8 | privacy §4 | Garmin's and Suunto's role (source on the user's instruction, not processors, as the text says) once the agreements are read |
| U9 | privacy §5 | The basis for the App Attest key on the iPhone (§ 25 TDDDG) |
| U10 | privacy §6 (Google Cloud, Firebase, Supabase, PostHog, Microsoft) | The transfer bases and terms, as the reviewer words them; the DPAs accepted in each console |
| U11 | privacy §6, §7 | Console facts: Cloud Trace storage and retention; Supabase log retention and its auth records; PostHog GeoIP, session recording and retention, and whether deleting an account deletes its events; the Microsoft tenant's data location; GoDaddy's access to the mailboxes; how long support emails are kept |
| U12 | `messages/en.json` `legal.contact.responseTodo` (not this PR's file) | The response time shown on `/support` |
| U13 | terms 2026-11-26 | That the changed terms are announced in the app before they apply (terms §12 says so; the app has no such notice at ea52758) |
| U14 | `deploy/run/config.env` `CONNECT_MIN_POLICY_VERSION` | Whether the consent cutoff moves to 2026-11-26 (D259; `docs/OPERATIONS.md` → Moving the consent cutoff: it moves when a change touches health-data processing, which this one does). This PR leaves it at 2026-10-26; no connection is live, so it has no effect today |
| U15 | the legal review as a whole | The questions of `docs/work/2026-11-20-privacy-c2/anwalt.md` (PR #228) that still apply, plus: describing features that are not yet available in a policy put into force (privacy §2); English only for Art. 9 consent |

## 3. Facts the texts depend on that need a fix elsewhere (not legal questions)

| # | What | Where |
| --- | --- | --- |
| P1 | The support page's step list says "Under Data, tap Export my data"; the app has no Data group, the button is "Export data" below the groups | `web/messages/en.json` `legal.steps.export` (PR A's area); app ea52758 `ProfileView.swift:502-506`, `DataExport.swift:6` |
| P2 | The screenshot's alt text names "Change password", which no longer applies | `web/messages/en.json` `legal.steps.alt` |
| P3 | The app still offers email sign-up, login and password reset | app ea52758 `Ryft/Auth/AuthService.swift:93-113`; an app change |
| P4 | `docs/SUPABASE.md` still describes email accounts as a sign-in method | platform docs |
| P5 | The deletion test counts no nutrition or coaching table (deletion relies on FK cascades) | `backend/internal/store/delete_test.go:74-91` |
| P6 | The in-app export leaves out journal, nutrition and connections (the text says "on request") | app ea52758 `DataExport.swift`; `GET /v1/me/nutrition-export` exists, unused |
| P7 | No guard refuses a `TODO(user)` in a published page: this PR's pages pass every check with 28 markers. A check that fails CI on `TODO(user)` in a `status: published` page would block this merge on purpose | `web/tests/legal.test.ts:104-107` (only drafts); proposal, not done here |
| P8 | The "Garmin [device model]" attribution in the app's screens and export, once connections ship | partners.md G9 |

## 4. The version (D290)

`2026-11-26` is the new privacy version: page, `privacy-versions.json`, `PRIVACY_POLICY_VERSION` in
`deploy/run/config.env` and `docker-compose.yml`, in one PR. It is `status: published` with `effective:
'2026-11-26'`, because `scripts/check_policy_in_force.sh` (CI's manifests job, against the base) refuses a PR
that moves the version to a page that is not published. **The merge puts it into force:** api and ingest roll
with the new version, the site job ships the page and checks that `/privacy` shows it, and every user is asked to
consent again before the next upload. Set `effective` to the merge date if it is later. 2026-10-20 and
2026-10-26 keep their pages and words. Terms move to 2026-11-26 the same way (no guard; 2026-10-20 stays at
`/terms/2026-10-20`).

## 5. Before the merge

1. Answer U1-U15; every `TODO(user)` resolved in the three files (and U12 in `messages/en.json`).
2. The legal review of privacy 2026-11-26 and terms 2026-11-26.
3. Google sign-in live in the app (U7), or the text changed to say what is live.
4. `npm run check:policy`, `npm run brand`, `npm test`, the build, the e2e tests, all green in CI.
5. Mark the PR ready and merge; the merge deploys (D290 lists the order).

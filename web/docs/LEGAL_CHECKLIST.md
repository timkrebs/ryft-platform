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

**Revised 2026-11-27.** The user's rule since then: legal questions don't block work. The most conservative sound
text is published now, open points stay `TODO(user)` (or a PR note), and a lawyer reviews it later (Linear RFT-155).
The revision brings the privacy text to today's RYFT (App Attest in `warn`, the no-watch morning check-in, BLS 4.0,
usage analytics; D290, "Amended 2026-11-27"). Sources: `docs/work/2026-11-26-legal-partners/revision-2026-11-27.md`.

**No `TODO(user)` renders on the site (decided 2026-11-27).** The 27 markers (24 in the content files: privacy
2026-11-26: 18, terms 2026-11-26: 2, Impressum: 3, support: 1; 3 in `messages/en.json`) were all page text except
`beta.missingEmail`, which shows only in a build without `SUPPORT_EMAIL` (`deploy.yml` refuses that build). They are
now tracked here (U1-U15) instead of on the pages: the Impressum names RYFT and the support email, with a postal
address for service on request by email, and no personal details; privacy §1 and terms §1 name "the provider named in the legal notice"; the transfer sentences keep
the facts the markers carried (Google's DPF certification and SCCs, Supabase's SCCs); Art. 77 names the member state
where you live or work; the rest of the markers were dropped with no replacement sentence (nothing invented). `/support`
no longer shows a response time (U12), and `/licenses` names "the provider named in the legal notice" as the rights
holder. The older pages have none.

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
- **Already published, now removed:** privacy 2026-10-20 and 2026-10-26 §1 and terms 2026-10-20 §1 carried your
  name and home address, and the Impressum on `main` carries them and your phone. This PR replaces them on the three
  archived pages with "[Provider details removed on 2026-11-27; the provider is named in the current legal
  notice.]" (wording otherwise unchanged; the original stays in git history as the record of consents, D290 item 3). The Impressum is a single page: this PR removes them
  from it, and offers a postal address for service on request by email. It is incomplete under § 5 DDG until a
  business or service address exists (Linear RFT-156 A1; U1, U5, U6). Git keeps the history either way.
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
| F8 | The food catalog is USDA FoodData Central (Foundation, SR Legacy), Open Food Facts and, since 2026-11-27, the BLS 4.0 of the Max Rubner-Institut (CC BY 4.0, imported; twins of curated staples left out). The attribution is on `/licenses` (Data sources); privacy §6, terms and support name it | D296; `scripts/etl/transform.py`; `docs/architecture/nutrition/06-data-source-licensing.md` |
| F9 | Usage analytics: PostHog EU Cloud (eu.i.posthog.com), opt-in, a random installation id, Profile → Privacy → Share usage analytics; the events, IP discarded and no location derived, the EU Standard Contractual Clauses in PostHog's DPA, 12 months. Privacy §7 is the app's text (`app/docs/ANALYTICS.md`) | D231; app `docs/ANALYTICS.md`; the user's answers of 2026-11-27 |
| F10 | Report a problem: Profile → About → Report a problem; diagnostics on by default, no health values | app ea52758 `Ryft/Features/Profile/ReportProblemView.swift:313, 325-346` |
| F11 | Export: account, days, workouts, sessions, manual entries; no journal, nutrition or connections | app ea52758 `Ryft/Features/Profile/DataExport.swift:14-33` |
| F12 | Deletion: everything, the Supabase user, the Apple grant, the connections at the providers (30 days of retries) | `backend/internal/store/store.go:163-192`; `docs/API.md:940-965`; D249, D257 |
| F13 | Garmin and Suunto connections: a foundation against a fake provider only; off in production | `backend/internal/connect/doc.go:18-19`; `deploy/run/config.env:52, 56`; `docs/API.md:997` |
| F14 | Minimum age 16 (terms 2026-10-20 said 13) | `backend/internal/scoring/maxhr.go:9`; app ea52758 `OnboardingSteps.swift:311` |
| F15 | App Attest goes to `warn`: a request without a valid check is logged (`reason`, `app_build`, route group, request identifier; no account, key or token) and still answered; `on` refuses it. Privacy §3 says both | `docs/OPERATIONS.md` §17.4; D245, D247 |
| F16 | The no-watch morning check-in is in the code behind a flag that is off; the app does not offer it yet. Privacy §2, §3, §5, §9, §12 carry the text of the implementation's privacy section; the parts that need the version describing N2R and coaching are not in it | D291-D295, D299; `docs/work/2026-11-26-no-watch-recovery/implementation.md` → Privacy |

## 2. Only you can answer (numbered; the marker in brackets)

| # | Where | What is needed |
| --- | --- | --- |
| U1 | Impressum "Provider", terms §1, privacy §1 | Provider and controller: name (or company) and an address for service, the same in all three (section 0) |
| U2 | privacy §1 | Whether a data protection officer is required (Art. 37 GDPR, § 38 BDSG); the text says none is designated |
| U3 | privacy §5, §7 | The review of every legal basis listed (the text proposes them), including the consent for usage analytics (Art. 6(1)(a) GDPR, § 25(1) TDDDG) |
| U4 | privacy §9 | The competent supervisory authority (follows from U1) |
| U5 | Impressum "Contact" | A second fast way of contact that is not your phone |
| U6 | Impressum § 18(2) MStV | The person responsible for the content |
| U7 | privacy §3, §6 (two markers) | **Google sign-in:** set it up in Supabase and ship it in the app before this version goes into force; then confirm what Google shares and Google's role. Until then the text promises a sign-in that doesn't exist, and email accounts made before 2026-11-26 can't sign in (support says nothing about them: your call) |
| U8 | privacy §4 | Garmin's and Suunto's role (source on the user's instruction, not processors, as the text says) once the agreements are read |
| U9 | privacy §5 | The basis for the App Attest key on the iPhone (§ 25 TDDDG) |
| U10 | privacy §6 (Google Cloud, Firebase, Supabase, Microsoft) | The transfer bases and terms, as the reviewer words them; the DPAs accepted in each console. PostHog is answered (2026-11-27): the DPA is accepted and the basis is the EU Standard Contractual Clauses (Art. 46(2)(c) GDPR; module 2 of the 2021/914 clauses). PostHog Inc.'s certification under the EU-U.S. Data Privacy Framework could not be confirmed, so the text does not name it. The reviewer may still reword it |
| U11 | privacy §6, §8 | Console facts: Cloud Trace storage and retention; Supabase log retention and its auth records; the Microsoft tenant's data location; GoDaddy's access to the mailboxes; how long support emails are kept. PostHog's are answered (2026-11-27): the IP address is discarded and no location is derived, session recording is not used (no screen recordings), retention is 12 months, and deleting an account does not reach the events because their random id is not linked to it (Art. 11 GDPR) |
| U12 | `messages/en.json` `legal.contact.responseTodo` (not this PR's file) | The response time shown on `/support` |
| U13 | terms 2026-11-26 | That the changed terms are announced in the app before they apply (terms §12 says so; the app has no such notice at ea52758) |
| U14 | `deploy/run/config.env` `CONNECT_MIN_POLICY_VERSION` | **Decided 2026-11-27:** the consent cutoff moves to 2026-11-26 in this PR (D259; `docs/OPERATIONS.md` → Moving the consent cutoff: it moves when a change touches health-data processing, which this one does; D290 item 9). No connection is live, so it has no effect today |
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
| P7 | No guard refuses a `TODO(user)` in a published page: this PR's pages pass every check with 24 markers. A check that fails CI on `TODO(user)` in a `status: published` page would block this merge, against the rule of 2026-11-27 (open points stay `TODO(user)`) | `web/tests/legal.test.ts:104-107` (only drafts); proposal, not done here |
| P8 | The "Garmin [device model]" attribution in the app's screens and export, once connections ship | partners.md G9 |

## 4. The version (D290)

`2026-11-26` is the new privacy version: page, `privacy-versions.json`, `PRIVACY_POLICY_VERSION` in
`deploy/run/config.env` and `docker-compose.yml`, in one PR. It is `status: published` (with a date in `effective`), because `scripts/check_policy_in_force.sh` (CI's manifests job, against the base) refuses a PR
that moves the version to a page that is not published. **The merge puts it into force:** api and ingest roll
with the new version, the site job ships the page and checks that `/privacy` shows it, and every user is asked to
consent again before the next upload. The version stays `2026-11-26` after the revision of 2026-11-27: it was never
in force anywhere, so the id is re-used. `effective` is `2026-11-27` (the planned merge day) on privacy and terms;
no guard ties it to the version id (`legal.test.ts` only wants a date; the live check reads the "Version" line). 2026-10-20 and
2026-10-26 keep their pages and words. Terms move to 2026-11-26 the same way (no guard; 2026-10-20 stays at
`/terms/2026-10-20`).

## 5. Before the merge, and after

Since 2026-11-27 (the user's rule): the pages are published as they are, and the open points below do not block the
merge. The `TODO(user)` markers stay in the text until the user answers; a lawyer reviews the published texts later
(Linear RFT-155).

1. `npm run check:policy`, `npm run brand`, `npm test`, the build, the e2e tests, all green in CI.
2. `CONNECT_MIN_POLICY_VERSION` is decided (U14: 2026-11-26); the merge asks every user to consent again.
3. Mark the PR ready and merge; the merge deploys (D290 lists the order).

Open after the merge, none of it blocking: U1-U15 (every `TODO(user)` resolved in the content files and U12 in
`messages/en.json`), the legal review (RFT-155), and Google sign-in live in the app (U7), or the text changed to
say what is live.

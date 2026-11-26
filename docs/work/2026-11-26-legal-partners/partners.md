# What the Garmin and Suunto partner programs ask of a public site

**Not legal advice.** This file lists what the two programs say they require of an applicant's website and
policies, where that was found, and whether ryftlab.de (as changed in this PR) meets it. Whether a point is met
in the legal sense is for the user and their reviewer.

Researched on 2026-11-26. WebFetch was refused on the first page tried, so **every item is from search-engine
results**, marked *(search)*: no page, agreement or PDF was read in full. Treat each line as a lead to check on
the page itself before applying.

"Where on the site" refers to this PR's files: privacy `web/content/legal/en/privacy/2026-11-26.mdx` (§ numbers
below), terms `web/content/legal/en/terms/2026-11-26.mdx`, support `web/content/legal/en/support.mdx`, legal
notice `web/content/legal/en/impressum.mdx`.

## Garmin Connect Developer Program

| # | Requirement (as found) | Source | Met? Where |
| --- | --- | --- | --- |
| G1 | The program is for business use; it mentions individual developers and start-ups, and Starter and Enterprise tiers. A separate Personal API exists for one's own data only | developer.garmin.com/gc-developer-program/program-faq, …/overview, …/enterprise/program-faq/, …/personal/personal-faq/ *(search)* | **Open.** The applicant's identity is `TODO(user)` in the legal notice, terms §1 and privacy §1. Whether an individual (not a company) is accepted: not found |
| G2 | A public website and a published privacy policy; Starter apps are shown as "unverified" until reviewed | the5krunner.com/2026/10/22/garmin-personal-api/ (a blog, not Garmin) *(search)* | Website and policy: yes (ryftlab.de, `/privacy`). The policy names the planned connection (privacy §4) |
| G3 | The privacy policy on the business's own site, not on an app-store page (an application was rejected for it and accepted after the move) | forums.garmin.com/developer/connect-iq/f/discussion/434542/ *(search)* | Yes: `/privacy` on ryftlab.de, linked from every page's footer (`web/src/components/chrome.tsx:71` on main) and from the app (app ea52758 `Ryft/Design/SafariView.swift:12-13`) |
| G4 | Applicant contact details on the site | the same forum thread (staff asked for the website and policy links) *(search)*; the form's fields: **not found** | Contact: `support@ryftlab.de` everywhere. Name and address: `TODO(user)` |
| G5 | End User Data used only as the law allows and "in compliance with each End User's consent"; the licensee indemnifies Garmin for breaches of privacy law | Garmin Connect Developer Program Agreement, www8.garmin.com/en-US/GARMINCONNECTDEVELOPERPROGRAMAGREEMENT/GARMINCONNECTDEVELOPERPROGRAMAGREEMENT_EN.pdf *(search)* | Text: privacy §4 (connect only on request, explicit consent, the data card before connecting), §5 (Art. 9(2)(a) proposed). The legal reading of the agreement: open (`TODO(user)`, privacy §4 Garmin's and Suunto's role) |
| G6 | Call `DELETE …/wellness-api/rest/user/registration` when the partner offers a "Delete My Account" or "Disconnect" outside Garmin Connect's own consent removal | Garmin OAuth2 PKCE guide, developerportal.garmin.com/sites/default/files/OAuth2PKCE.pdf *(search)* | Designed, not built: disconnect and account deletion make one attempt to end the grant and retry for 30 days (D257, `backend/internal/store/connections.go:33-36`, `docs/API.md` → `DELETE /v1/me`). No Garmin adapter exists on main (`backend/internal/connect/doc.go:18-19`). Text: privacy §4, §7; support "Garmin and Suunto" |
| G7 | `GET user/permissions` for what the user granted; the "User_Permission" webhook for later changes; partner verification checks deregistration and permission endpoints | the same guide *(search)*; a developer forum report *(search)* | Designed: inbox kinds `deregistration` and `permissions` (connections design §7.3; `backend/cmd/ingest/connect.go:360-372`); Garmin's real notice format is phase 2 (D257). Text: privacy §4 (a revocation at Garmin deletes the data) |
| G8 | Deleting data after a deregistration | **not found** in Garmin's pages | RYFT's own commitment: privacy §4, §7 (D259 decision 2) |
| G9 | Visuals of Garmin data (charts, maps, social images) carry "Garmin [device model]"; screenshots, printouts and exports keep it; it covers derived data; Garmin may review and suspend | Garmin Developer API Brand Guidelines, developer.garmin.com/downloads/brand/Garmin-Developer-API-Brand-Guidelines.pdf, developer.garmin.com/brand-guidelines/overview/ *(search)* | **App, not site.** Nothing on the site shows Garmin data. The app's screens and the export need it once connections ship (open, checklist P8) |
| G10 | The Garmin app name and tile in full, never abbreviated or stylised; the tag logo unaltered | the same PDF *(search)* | The site uses only the word "Garmin" (and "Garmin Connect") as a data source, no logo. Brand rule: `web/CLAUDE.md` |
| G11 | The "Works with Garmin Connect" badge only for a commercially available integration, at least 88 px high | developer.garmin.com/brand-guidelines/connect *(search)* | Not used, and must not be until the integration is live |
| G12 | A trademark notice for developers | **not found**. Garmin's manuals use: "…are trademarks of Garmin Ltd. or its subsidiaries. These trademarks may not be used without the express permission of Garmin." (www8.garmin.com/manuals/webhelp/bc35/EN-US/GUID-5481E1FD-F39B-47B2-8500-47790A80DC1C.html) *(search)* | No notice added: nothing mandated was found, and the site names Garmin only as a source. If the user wants one, the sentence above is a model, not a requirement |
| G13 | What the privacy policy must contain | **not found** in any Garmin source | — |
| G14 | Program status: third-party posts of October 2026 report that Garmin paused or tightened third-party access | tryterra.co/blog/garmin-connect-developer-program-pause, the5krunner.com/2026/10/14/garmin-api-intervals-third-party-apps/ *(search)*; not confirmed on Garmin's pages | Check before applying |

## Suunto (API Zone, partner program)

| # | Requirement (as found) | Source | Met? Where |
| --- | --- | --- | --- |
| S1 | Access for companies and organisations; no API access for personal use. Apply through the partner form ("Suunto Cloud API"), accept the API agreement, name the developers for API Zone; answer in about two weeks | apizone.suunto.com/faq *(search)* | **Open.** The applicant is `TODO(user)` (legal notice, terms §1, privacy §1). Whether a private individual qualifies is doubtful on this wording; the reviewer's and the user's call (company question, LEGAL_CHECKLIST section 0) |
| S2 | Company details, the partnership model, commercial or marketing plans | us.suunto.com/pages/welcome-partners, www.suunto.com/welcomepartners *(search)* | Company details: `TODO(user)` |
| S3 | Production access after submitting the app's information in the API Zone profile, which Suunto checks | apizone.suunto.com/faq *(search)* | Not yet applicable |
| S4 | A privacy policy or terms URL as a requirement | **not found** | `/privacy` and `/terms` exist |
| S5 | OAuth2, each Suunto user authorises the app | apizone.suunto.com *(search)* | The connections foundation is OAuth with PKCE (D257); no Suunto adapter on main |
| S6 | Implement the deauthorize operation so that a disconnect on the partner's side shows in the Suunto app | apizone.suunto.com/docs/services/oauth2-api/operations/deauthorize; forum.suunto.com/post/53397 (Suunto staff) *(search)* | Designed generically (the deregistration queue, D257); a Suunto adapter is not built. Text: privacy §4, support |
| S7 | Suunto's EU Data Act notice: when a user ends a partner connection Suunto deletes or anonymises its related data; data already shared stays with the partner, and the user asks the partner to delete it | www.suunto.com/legal-pages/suunto-eu-data-act-notice/ *(search)* | Privacy §4, §7 and support: disconnecting deletes what RYFT received; deletion requests to `support@ryftlab.de` |
| S8 | A deauthorisation webhook to partners, retention, security or resale rules | **not found** (the API agreement is handed out only through the application) | — |
| S9 | Logo rules (no modification, clear space, colour versions); "registered or unregistered trademarks of Suunto Oy" | brand.suunto.com; www.suunto.com/Support/Product-support/suunto_7/suunto_7/reference/trademark/ *(search)* | No logo used; "Suunto" only as a source |
| S10 | A "Works with Suunto" badge or attribution wording | **not found** | — |

## Still missing for an application (both programs)

1. The applicant's identity and address (G1, G4, S1, S2): `TODO(user)`, LEGAL_CHECKLIST section 0.
2. A real adapter per provider, with deregistration (G6, S6) and Garmin's permission and deregistration
   notices (G7) in their real formats: phase 2 of the connections design; none on main.
3. The "Garmin [device model]" attribution in the app and its export (G9).
4. The reviewer's reading of each agreement (G5, S8), which only the full texts allow.
5. Whether Garmin's program accepts new partners now (G14).

## Not verified

- Every item above: no page was read, only search results.
- Garmin: whether an individual is accepted, the application form's fields, the agreement's clauses on
  deletion, retention, sharing, security and termination, a mandated trademark notice, the exact attribution
  layout, what a privacy policy must contain, the program's current status.
- Suunto: the API agreement (privacy, deletion, branding, individuals), a deauthorisation webhook, a "Works with
  Suunto" program.

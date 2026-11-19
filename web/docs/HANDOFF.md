# Handoff — what the other two folders need from web/

> **Historical.** This file describes the two-repository era (`ryft-web` next to `ryft-app` and `ryft-platform`). Since the move into `plattform/web` (platform D107) there is nothing to hand over between repositories; paths and steps below are kept as a record only. Section 2, the platform's switch to the site's container image with its two patches, went with the cluster and the image (platform D246, web D25); the history before that PR holds it.

The site in `web/` replaced the platform's ConfigMap site on its cluster. This file had three
parts: what **you** decide or click, the change for the **platform session** (removed, see
above), and what the **app session** needs. Nothing outside `web/` was edited.

As of 2026-10-21.

---

## 1. For you

### Done on 2026-10-21

- **The repository is public** (your choice). Before that: no personal details in the
  tree, history or PRs; gitleaks clean; the fonts are never committed.
- **`main` is protected:** ruleset `main` (id 25564428): PRs only, squash only, linear
  history, no force push or deletion, the four checks required and up to date, nobody
  bypasses it. Merge commits and rebase merges are off; branches go on merge.
- **`SUPPORT_EMAIL` = `support@ryftapp.de`**, from the next release on (support page, the beta
  page's "Ask for an invite"). `TESTFLIGHT_URL` stays unset: the beta is invite-only, so the
  "Open in TestFlight" buttons stay hidden.

### Mail for ryftapp.de (later, by your choice)

`ryftapp.de` has no MX record today, so `support@ryftapp.de` receives nothing yet, and mail
to it bounces. The site from `f437747` on prints that address (support page, the beta
page's "Ask for an invite").

1. **A mail service** for the domain. It should receive mail for `support@ryftapp.de`,
   forward it to your private inbox, and let you **reply as** `support@ryftapp.de`: replies
   from your private address would show it to everyone who writes. It needs a data
   processing agreement, and ideally servers in the EU. It is named in privacy §5 (the
   marker is in ryft-web PR #10).
2. **The forwarding target** goes only into that service's settings, never into DNS text
   records, a repository or a variable. Avoid services that keep the target in a public
   TXT record.
3. **DNS:** give the platform session the records the service asks for (MX, SPF, DKIM,
   DMARC, a verification TXT), for the domain's zone.

### Grafana Cloud: exclude RYFT's logs (your choice)

Grafana Cloud → Fleet Management → the pod-log pipeline: exclude namespaces matching
`ryft-.*`. Then Explore → `{namespace=~"ryft-.*"}` over the last hour must return nothing.
The privacy text now says Grafana receives service metrics only (PR #10), which is true only
from then on. (The cluster and its collector are gone since platform D246.)

The image's visibility (D14) and how its releases reached the cluster were the other two items
here; both went with the image (web D25).

### Legal pages

`docs/LEGAL_CHECKLIST.md`: what is answered, what is still open, and the facts with their
sources. Your answers of 2026-10-21 are in ryft-web PR #10; the pages stay drafts until every
`TODO(user)` is resolved and you say "publish".

### Brand findings in app/ and plattform/

`docs/BRAND_CHECK.md`: **nothing user-facing** carries the denylisted name; one user-facing
warning (a planned feature name in `app/docs/APP_STORE.md:64`); 48 internal hits in
comments and docs, which are your call. Both repositories are public, so the most visible
is `plattform/README.md:3`, the platform's front page on GitHub.

---

## 2. Paste into the platform session (plattform/)

Removed (platform D246, web D25). It switched the platform's cluster from the ConfigMap site to
the site's container image, pinned its releases there, and came with two patches for the
platform. The cluster, the image and the patches are gone; Firebase Hosting serves the site
(web D19). What still concerns you from it is in section 1: mail for the domain and the
Grafana Cloud logs.

---

## 3. Paste into the app session (app/)

> **ryftapp.de moves to a new build; every URL the app uses stays the same.** What the app
> links to (checked against `ryft-app` `main`, 2026-10-21):
>
> | URL | Where in the app | On the new site |
> | --- | --- | --- |
> | `https://ryftapp.de/privacy` | `Design/SafariView.swift`, `Features/Auth/WelcomeView.swift`, `CreateAccountView.swift`, `Localizable.xcstrings` (en, de) | 200, the privacy policy (version 2026-10-20) |
> | `https://ryftapp.de/terms` | `Design/SafariView.swift`, `CreateAccountView.swift`, `Localizable.xcstrings` | 200 |
> | `https://ryftapp.de/auth/callback` | `App/SupabaseConfig.swift` (`universalRedirectURL`) | 200, "Open this in RYFT", `no-store`, no script reads the URL |
> | `https://ryftapp.de/<path>` universal links | `Navigation/AppRoute.swift` | 200 "Open this in RYFT" for `/day/*`, `/sleep/*`, `/insights/*`, `/train/*`, `/auth/*`, `/invite/*` |
> | `https://ryftapp.de/.well-known/apple-app-site-association` | (Associated Domains, not added yet) | `application/json`, no redirect; every path `AppRoute.url` creates, **now including `/invite/*`** (your `API_REQUESTS.md` → Universal links, 2026-10-21) |
> | `https://ryftapp.de/support`, `https://ryftapp.de` | `docs/APP_STORE.md`, `docs/TESTFLIGHT.md` (App Store Connect fields) | 200 |
>
> Once the new site serves the AASA, add
> `applinks:ryftapp.de` under Signing & Capabilities → Associated Domains, as your
> `API_REQUESTS.md` plans; ryft-web's test checks the AASA against `AppRoute` on every PR.
>
> **Requests from the legal review:**
>
> - **Minimum age:** the privacy policy and terms say 13, which is what the app accepts
>   today (`latestBirthYear`, `− 13`). Nothing to change.
> - **Saving workouts to Apple Health** (your beta feedback brief, section 3): the privacy
>   policy already says, in English and German: "If you turn on saving workouts, it also
>   writes your completed strength workouts to Apple Health (the type, start, end and energy,
>   if known; no heart rate)." If what ships differs (more fields, heart rate, a default other
>   than off), tell ryft-web before the release.
> - **"Profile → Report a problem"** is named in `docs/TESTFLIGHT.md:85`, but the app has no
>   such screen yet (`POST /v1/feedback` is in `APIClient` and `Models.swift`, nothing calls
>   it). The support page will name whatever exists when it is published: build the screen,
>   or change the doc to TestFlight's screenshot feedback.
>
> Optional:
>
> - New captures for the website, in dark **and** light, as 1206 × 2622 simulator
>   screenshots in `docs/screenshots/`: `today-{dark,light}.png` (the current Preview
>   captures show the readiness number missing or overlapping mid-animation, so the site
>   draws its Today hero natively); `journal-{dark,light}.png`; `profile-data-{dark,light}.png`
>   (Profile → Data with "Export my data"). The website picks them up with
>   `npm run screenshots`.
> - The in-app Licences screen and `ryftapp.de/licenses` list the same Swift packages
>   (ryft-web's CI fails when `Package.resolved` changes without its snapshot; synced for the
>   bump in ryft-app #7). If a package is added, run `npm run licenses -- --sync` in ryft-web.

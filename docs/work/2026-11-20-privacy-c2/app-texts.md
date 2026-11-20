# C2: the app's texts that must match the new policy (listed, not edited)

Read on 2026-11-20 in `ryft/app` (the working copy, "WC") and `ryft-app-main` (a main checkout of 2026-11-17,
"MAIN"). The app session owns every change here.

| Screen / string | File | Matches 2026-11-20? |
| --- | --- | --- |
| Health consent: "Your health data, your consent"; the 10 types it reads; "In your RYFT account, on servers in the EU. Never sold, never used for advertising."; "Only you, and the operator of RYFT when something needs fixing."; "withdraw this consent in Profile → Privacy"; checkbox "I agree that RYFT processes my health data to calculate my scores."; "Nothing is synced until you agree. Privacy policy version {v}." | WC `Features/Onboarding/OnboardingSteps.swift:167-224`; MAIN `HealthPermissionView.swift:700-751` | **Yes.** Google Cloud (Belgium) and Supabase (Frankfurt) are both in the EU. Whether "the operator … when something needs fixing" covers what §8 now says is question 10 for the lawyer. |
| "You must be at least 16 to use RYFT." | same, `:190` | Yes (§9) |
| The consent link: `privacy_policy_url` from `GET /v1/config`, accepted only on `ryftlab.de` | WC `OnboardingCoordinator.swift:238`, `Design/SafariView.swift:12,19` | WC yes. **MAIN checks `ryftapp.de`** (`SafariView.swift:12,19`) |
| Links to the policy and terms: Welcome, Create account ("By creating an account you agree to the Terms and the Privacy Policy"), Profile → Privacy, Profile → About | WC `WelcomeView.swift:101`, `CreateAccountView.swift:31`, `ProfileView.swift:414, 676-677` | WC yes (`ryftlab.de/privacy`). **MAIN: `ryftapp.de` everywhere** (`SafariView.swift:12-13`, `WelcomeView.swift:84`, `CreateAccountView.swift:31`, `AuthComponents.swift:202`, `SupabaseConfig.swift:34`); MAIN may just be stale |
| Info.plist health read string | `Ryft/Info.plist:79` | Yes |
| Info.plist health write string: "RYFT saves your strength workouts to Apple Health, only if you turn it on. It writes nothing else." | `Ryft/Info.plist:81` | Yes (§2 now says no energy) |
| **PostHog opt-in** | none | **Missing: the app has no analytics code, toggle or opt-in screen** (WC and MAIN). §3's markers (event list, identifier, the menu path) wait for it. When it is built, its strings must say: off until you agree, a fixed list of usage events, a pseudonymous identifier, no health data, PostHog EU Cloud, withdrawable in settings. |
| App Attest | WC `Networking/AppAttestSeams.swift`, `AppAttestor.swift`, `Ryft.entitlements` | No user-facing text (none needed by §3); **MAIN has no App Attest** |
| Report a problem: "Include diagnostics" (on by default), "What's included", "No health values: …" | WC `Features/Profile/ReportProblemView.swift:240-297`, `ProfileViewModel.swift:1820` | Yes: §3 lists the same items |
| Withdraw consent: Profile → Privacy → "Withdraw consent" | `ProfileView.swift:402, 116-125` | Yes (§7) |
| Delete account: Profile → Account → "Delete account" | `ProfileView.swift:105-111, 202` | Yes (§6, §7) |
| Export: Profile → Data → "Export my data" (JSON) | `ProfileView.swift:519-520` | Yes (§7 names its scope) |
| `PrivacyInfo.xcprivacy`: tracking false; Health, Fitness, Email, Name, User ID, Device ID, Other User Content, Other Diagnostic Data, all App Functionality | `Ryft/PrivacyInfo.xcprivacy` | Yes today. **With PostHog it needs "Product Interaction", purpose Analytics**, and App Store Connect's privacy labels likewise |

Not yet on the platform, so not in the text: the app's manual activities (`PUT /v1/activities`: notes, an optional
GPS route, heart-rate samples, kept 30 days on the phone; WC `Activities/ActivitiesAPI.swift:9`,
`Models.swift:2603-2628`). When the platform accepts them, the policy needs a new version.

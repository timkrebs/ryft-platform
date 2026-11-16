# Review: `config: analytics` (D231)

## security-privacy-reviewer (2026-11-16, read-only)

No blockers. The key reaches no log line, error, metric label or Cloud Run config. Every `LoadAnalytics` error
is a fixed string, and `url.Parse`'s error, which quotes its input, is not wrapped. `ryft-api` is the only
accessor. `check_cloudrun.py` refuses `latest`, other services, another secret under the name, and a plain value.
Nothing on the server sends to PostHog.

| # | Finding | Severity | Resolution |
| --- | --- | --- | --- |
| 1 | `check_cloudrun_test.sh`'s PostHog cases add the pin to a copy that will already hold it after the pin PR ("set twice"), so that PR's CI goes red; the runbook only ran `make cloudrun-check` | should-fix | Every case starts from a copy without the pin (`nopin`). Run with the pin in `api.yaml` and without: both pass. The runbook's step 3 runs the test too. |
| 2 | `check_api.py:426` and `check_accounts.py:121` print the whole config, `project_key` included, on failure (also against production with `API_URL`) | should-fix | Both print the field names or the two policy fields only |
| 3 | The host check let through `,` `"` `<>` in a host, IP literals, a trailing dot and `HTTPS://`; D231 claimed `POSTHOG_HOST` is a reviewed value in `deploy/run`, but `check_cloudrun.py` doesn't admit it | nit | `https://<host name>` exactly, with a hostname pattern (two or more labels, the last starting with a letter), and the raw string must equal `https://` + host. Tests for each form. D231 now says production uses the default until a PR adds the variable and its check. |
| 4 | Key fixtures were letters and digits only | nit | Added a URL-safe key (`-`, `_`) and the 128-character upper bound as accepted, and 129 characters and `phs_` as refused |
| 5 | "No IP" rests only on PostHog's *Discard client IP data* | nit | Added to the runbook's step 2 and its **Stop if** |
| 6 | Serve `analytics: null` when the flag is off for the caller, so the kill switch also covers a buggy app build | consider | Not done: the plan keeps the flag and the field independent (the app combines them; D231 → Alternatives) |

For the privacy text (the analytics plan's step, not this PR):
- PostHog as a processor with a DPA, and the transfer basis (PostHog Inc. is a US company, even on EU hosting).
- Consent under Art. 6(1)(a), and §25 TDDDG for the identifier on the device.
- That the IP is sent but discarded, and how long PostHog keeps data.
- That no event or property carries health values (Art. 9).
- Whether account deletion removes the PostHog person (Art. 17). If it does, that is a new server-to-PostHog flow and a new secret.
- How the opt-in is evidenced (Art. 7(1)).

## code-reviewer (2026-11-16, read-only, after main with #189 was merged in)

One blocker: CI's lint was red. The migration, the contract (`analytics` always present, nullable in the
snapshot, strict decode of both examples), the merge resolution (main's English-only `privacy_policy_url` row
kept; changelog rows newest first; D230 before D231), the optional pin in `check_cloudrun.py` and the
Terraform test all held up.

| # | Finding | Severity | Resolution |
| --- | --- | --- | --- |
| 1 | `scripts/pkgmap.sh --check` (lint) fails: the package map's test-file counts are stale (cmd/api 35→36, internal/config 2→3, internal/db 15→16) | blocker | `docs/ARCHITECTURE.md`'s three rows updated to CI's diff, line for line |
| 2 | The bootstrap runbook still counts 11 secrets: "116 to add" (step 2f, step 4 and its **Stop if**), "11 secrets + 11 accessor bindings", "The 11 containers" (step 5). A rebuild would stop at step 4 | should-fix | 118 to add, 12 + 12 = 24, the running totals, "The 12 containers", and `infra/gcp/README.md`. Step 5 gets `put posthog-project-key`, needed before step 8 once `api.yaml` pins it |
| 3 | The flag's runbook paragraph lets the operator allow-list testers before the privacy policy in force names PostHog | should-fix | A **Stop if**: the policy in force names PostHog (EU Cloud) as a processor, with the purpose and the opt-in, and *Discard client IP data* is on, with a `curl … \| grep -c PostHog` check. STATUS says the same |
| 4 | #191 and #192 both append a D-entry after D230 and conflict | nit | Whichever merges second merges main in (D231 before D232) |

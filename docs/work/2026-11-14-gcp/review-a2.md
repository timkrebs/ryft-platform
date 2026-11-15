# Review of A2 (2026-11-15)

One round on PR #171 by code-reviewer and security-privacy-reviewer, in parallel. A fix agent started on the findings
and stopped on a network error. The lead finished the fixes. The six-agent budget was spent, so no reviewer re-checked
them; the tests named below cover each one.

## Findings and what happened

| # | From | Severity | Finding | Outcome |
|---|---|---|---|---|
| 1 | code | blocker (CI) | `TestAdminLogsHoldNoTokensEmailsOrAddresses` refused the new `client address` line (`client_ip`, `remote_host`) | fixed under head-architect's rule, **no IP address in any log line**: the line holds `picked`, `xff_index` and `xff_entries`, no address and no /24 or /48. The keys are in both allowlists with their reason (D222) |
| 2 | security | major | `ClientIP` skipped private, CGNAT and link-local entries as hops, so a client inside a VPC (Private Google Access, Direct VPC egress) would be keyed by the forged entry to its left | fixed: only Google's front-end ranges are skipped; any other entry is the client as it is (`TestClientIP`'s VPC cases, `TestStepNineUnderGoogleFrontEnd`) |
| 3 | security | major (privacy) | the line logged the picked address and the peer; the privacy text in force promises logs without IPs | fixed with #1. Runbook step 9 test 2 now sends three forged entries and checks `xff_index` is 3 |
| 4 | security, code | major | a refused login on Cloud Run ended the start: a crash loop where every new instance logs in once more and keeps Supavisor's breaker shut for the whole role, and no tick ever reports `ryft_db_up 0` | fixed: the instance stays up and holds; requests get the 503 at once; a tick reports the database down; a due tick (5 minutes since the last login) gets one retry, bounded to 20 s (`db.LoginRefused`, `app.holdLogin`; `TestARefusedLoginHoldsAndATickRetries`, `TestASignalWhileTheStartHolds`; D221) |
| 5 | security | major | a flood from one IPv6 /48 fills the admin limiter's 4,096 slots and drains the shared bucket, locking out `ADMIN_API_KEY` too | fixed: a request bearing the key (constant-time check) passes before the limiter; a wrong key is limited as before (`TestAdminKeyPassesTheLimit`; D222) |
| 6 | code | major | the outbox's flush, mark and rollback and the tick's final push ran on `context.WithoutCancel`, past the shutdown's cut, so a connection could still be out when the pool closed | fixed: `httpx.Detach` (budget or the server's hard stop) in `outbox.detach` and the tick's `Finally` (`outbox_test.go`, `tick_test.go`) |
| 7 | code | minor | "the transport dials nothing": opening it waits for the first fetch of Google's JWKS (up to 10 s) | fixed in `app.go`, `eventbus.go`, D220 and ARCHITECTURE: the port may open up to 10 s late, within the startup probe's 20 s |
| 8 | code | minor | API.md's local readyz row was scoring's and notify's; api and ingest answer `httpx.Health`'s body | fixed: one row each; the changelog entry says the same |
| 9 | security | minor | `postgres:17-alpine` in compose and CI was pinned by tag only | fixed: by the index digest, as the emulator and curl |
| 10 | security | minor | `scripts/smoke.sh` puts the smoke user's password and token on argv | **deferred**: local and runbook use only. A follow-up moves them to stdin, as step 9's 33 MiB test already does |
| 11 | security | minor | Dependabot's docker entry covers `backend/`'s Dockerfile, not the digests in `docker-compose.yml` and `ci.yml` | **deferred**: a move of those digests is by hand, both files together (their comments say so) |

## Privacy (security-privacy-reviewer)

The policy in force needs no change for this merge: nothing is served until step 9. Two conditions belong to step 9,
not to this PR. C2's text must be in force before apply 2 opens the invokers, and Google Cloud's data processing terms
must be accepted. Cloud Run's own request log holds client addresses for 14 days. That is Google's record, not one of
our log lines, and C2 must name it.

## Not in A2

- No `scoring-rescore` job in `deploy/run` yet (design §9; its env is in D222).
- C1 removes CI's EKS `images`/`promote` jobs.

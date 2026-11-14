# Review of the GCP design (2026-11-14)

One read-only review by security-privacy-reviewer of `design.md`, then a narrow re-check of the fixes.

## Findings and what changed

| # | Rank | Finding | Fix in `design.md` |
|---|---|---|---|
| 1 | blocker | The privacy change list would leave the text in force false. It doesn't name IP and user agent in the technical logs. Logs were 30 days, but the text says 14. "Visiting" says the IP is not stored and is limited by us. "Can reach only what it needs" is false with open egress. Firebase's own terms aren't named | §6: the bucket keeps 14 days, plus a note on `_Required`, Trace and Error Reporting. §8 C2: the four corrections and the Firebase terms. §5.2: the "reach only" gap |
| 2 | blocker | Admission policy P5 (`p05-auth-env`) is dropped. Nothing on Cloud Run would refuse `AUTH_MODE=apikey`, `ALLOW_INSECURE_APIKEY_AUTH` or `USER_ID` on a public service | §5.1: `config.LoadAuth` refuses them when `K_SERVICE` or `CLOUD_RUN_JOB` is set, and a CI check covers `deploy/run/*.yaml` |
| 3 | should-fix | The WIF condition used names and pinned no workflow. The HCP pool wasn't pinned. `hcp-terraform` can grant itself secret access | §5.1: repository and owner IDs, `job_workflow_ref`, `event_name`, environment. §11 step 2: organisation and workspace IDs, separate plan and apply SAs. Added to "Left over" |
| 4 | should-fix | Secrets on `latest`: a cold start reads a new version before `ALTER ROLE`, which trips the circuit breaker for the whole role. One `versions add` changes the data key | §4: every secret is pinned by version number, with a reviewed rotation. §5.3 |
| 5 | should-fix | `sslmode=require` doesn't verify the server certificate, and the link now crosses the internet | §4: `verify-full` with Supabase's CA in the image |
| 6 | should-fix | The admin limiter's map has no cap (AAAA is likely). A Google hop in `X-Forwarded-For` would put every client in one bucket | §5.1: PR A caps the map. B0 sends a forged XFF |
| 7 | should-fix | OIDC only on the public services. No explicit audience, `sub`, `exp` or RS256. Reusing the Supabase parser. Replay isn't stated | §2.2: all four services, explicit audience compared with config, RS256 + `exp` + `sub`, the keyfunc only, `Authorization` never logged, the replay window stated |
| 8 | should-fix | The budget doesn't bound Hosting's egress (Blaze). Billing data lags. The next apply re-adds `allUsers` | §7: `firebase hosting:disable`, `public_invokers = false`, the lag. §13 item 9 offers a second project without billing |
| n | notes | `/readyz` leaks `err.Error()`; `rm -P` does nothing on APFS; the demo password; CAA in the DNS compare; notify's outbox grant | §3, §5.3, §11 step 4, §2.2 |

Verified as sound by the reviewer: secret-level bindings, D99 kept through one topic per key, the EU storage policy,
and the data key's fingerprint step (the script hashes the value without a newline, as `versions access` returns it).

## Re-check (the same reviewer, read-only)

B2, S3, S5, S6, S7, S8 and the notes are closed. Still open, now fixed:
- **S4:** `migrate` sets every role's password on every run (`cmd/migrate/main.go`), so pins that differ between
  `migrate.yaml` and `<svc>.yaml` would set a role back on the next deploy. Rotation is now one commit that moves both
  pins, with a CI check that they agree, and `deploy.yml` runs migrate before the roll. The data key's fingerprint
  check reads version `<N>`, not `latest` (§4, §5.3).
- **B1:** Cloud Trace is named in the privacy draft's processor list (§8).
- One-line edits: `API_KEY` added to the P5 check. `hcp-terraform-apply` named in "Left over". Cost-DoS split into
  Cloud Run and Hosting (§5.2). `hosting:disable` takes the legal pages offline (§7). B0 gets the `verify-full` login
  on both pooler ports (§10).

The reviewer confirmed the replay statement: notify reads the score from the database, so an injected event can't
put its own numbers into a push. Its verdict was "not ready until S4 is in", and S4 is now in. These last fixes
weren't re-reviewed a second time.

## The user's answers (2026-11-14): no new review

The brief asked for a new review only if a decision changes the security picture. None widens it:
- **No site** removes Firebase Hosting: one processor, its egress (S8's Hosting half is moot) and the
  `hosting:disable` step are gone. The privacy consequence is stated, not reviewed away: REBUILD step 12 still
  holds, so only the user signs in until ticket F (§8). C2 stays a draft and goes into force with F.
- **Ingress internal** on scoring and notify (found while checking B0) narrows their exposure further. The in-app
  OIDC check (S7) stays.
- **The Grafana key** (accepted), **`min-instances 0`** and **§5.2's gaps** were already in the reviewed design.
- **HCP Terraform in `ryft-lab`:** §11 step 2 now writes out S3's reviewed fix as commands: the condition on the
  org and workspace IDs, a plan SA and an apply SA, each bound to its run phase. Stated in the text: the apply SA is
  owner-equivalent in effect through `projectIamAdmin`, which is inherent in Terraform owning IAM; the boundary is the
  condition plus `auto-apply` off. The HCP token goes through stdin, never argv; the old org's token only as a
  one-command prefix, kept out of shell history.
- **The budget** by hand with a 10 % rule, and the stop line without Hosting, change no exposure.

These edits weren't reviewed by a second agent. If PR B's implementation changes the bootstrap's roles or the
condition, its security-privacy review covers them there.

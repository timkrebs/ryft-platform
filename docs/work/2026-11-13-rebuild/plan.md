# Rebuild of production with Vault CE in the cluster: plan (platform-lead, 2026-11-13)

Brief: head-architect's "rebuild production as it was, with Vault CE in the cluster" (2026-11-13). The root plan
`../docs/work/2026-11-13-rebuild/plan.md` could not be read from this session (outside the allowed directory); the
state below is the brief's and this clone's.

## State found

- `main` at `f0fc3bc` (#155). The decommission branch `infra/decommission` (D206, `docs/DECOMMISSION.md`) is **not in
  this clone**; D206 stays reserved for it. New decisions start at **D207**.
- Every RYFT AWS resource is gone; the HCP Terraform workspaces of `infra/aws`, `infra/aws/kubernetes` and `infra/vault`
  hold empty states (their earlier state versions are the only copy of the old `DATA_ENCRYPTION_KEY`). `infra/aws/dns`
  still holds the zone `Z08455293REDHBC9QW5MZ` and its records; the domain is delegated to GoDaddy.
- HCP Vault and Google Workspace are deleted. Supabase (testers' data, roles), the Apple keys, Grafana Cloud, the GitHub
  `deploy` environment and the GHCR images pinned in `overlays/eks` remain.
- Main's Vault CE work (#145, #146, #147, #151, #153, #155; D186-D205) is merged and was never applied. It assumes a
  migration from HCP (dual-write, cutovers 5a-5i). A rebuild from empty needs none of that.
- Local `terraform.tfvars` of the three roots are present (git-ignored). `infra/vault`'s still sets
  `vault_parent_namespace_root` and `vault_address_for_cluster` (HCP).

## Decisions taken here (the user confirms the four marked **user**)

| # | Decision | Built | D |
| --- | --- | --- | --- |
| 1 | The in-cluster Vault is the only Vault: `infra/vault` drops HCP (provider, namespaces, per-namespace auth mounts, reviewer JWTs), writes the in-cluster Vault always, and every generated VaultConnection reads `https://vault.vault.svc:8200`. No dual-write, no cutovers, no `vault_diff` HCP mode | yes | D207 |
| 2 | **user** Seal: Shamir by default (5 shares, threshold 3, shares and the initial root token into 1Password). `vault_seal = "awskms"` keeps D189's auto-unseal one variable away; the unseal KMS key stays in `infra/aws` (about $1 a month) so that switch needs no `infra/aws` apply, only an `infra/aws/kubernetes` apply and `vault operator unseal -migrate` | Shamir + switch | D208 |
| 3 | **user** Operator login without Google Workspace: no OIDC in the rebuild. A session is `vault operator generate-root` with three shares, a 1-hour admin token and the terraform token from it, root revoked. Alternatives: the root token kept in 1Password (simpler, never expires), or OIDC with a personal Google account later (additive; the bootstrap files stay) | no OIDC | D209 |
| 4 | **user** (G2) Gates: G3 and G4 block the bootstrap (G4 is in code; G3 is procedure). G1 does not block a rebuild "as it was" (the admission chart was disabled in production; under Shamir no pod in `vault` can reach a seal key); follow-up: chart on in Audit, then the `vault` bindings to Deny. G2 is the user's | docs | D210 |
| 5 | **user** `DATA_ENCRYPTION_KEY`: recovered from the last state version of `ryft-prod-euc1-vault` before the destroy and imported into `random_bytes.data_key["api"]` before the first apply (the value never printed, nothing to remember on later applies). If the workspace is gone: a one-off statement clears the unreadable tokens; no migration | script + runbook | D211 |
| 6 | **user** Mail: `infra/aws/dns` mail records become variables with a no-mail default (null MX `0 .`, `v=spf1 -all`, DMARC `p=reject`, no DKIM, no site verification); the Google values stay as an example. `support@ryftapp.de` is named by the Impressum and the privacy policy: mail must work again before or soon after the testers return | yes | D212 |

## Tickets

| Ticket | Owner | Files (one writer each) | Wave |
| --- | --- | --- | --- |
| R1 `infra/vault` in-cluster only | infra-engineer | `infra/vault/**`, `deploy/k8s/overlays/vault-namespaces/**` (generator output), `docs/OPERATIONS.md` → `## Vault` section only | 1 |
| R2 add-ons and DNS | infra-engineer | `infra/aws/kubernetes/**`, `infra/aws/dns/**`, `infra/aws/**` if needed (not `terraform.tfvars`) | 1 |
| R3 data key and Supabase | database-engineer | `scripts/recover_data_key.sh`, `scripts/tests/recover_data_key_test.sh`; returns the Supabase and D93 findings and the fallback statement | 1 |
| R4 scripts and manifests off HCP | gitops-engineer | `scripts/lib/admin.sh`, `scripts/seed_demo.sh`, `scripts/vault_diff.sh` and their tests, `deploy/**` except the generated overlays, `.github/**` if needed | 1 |
| R5 alerts, monitoring, steps 9-13 | sre | `deploy/grafana/**`, `deploy/monitoring/**`, `docs/OBSERVABILITY.md`, `docs/OPERATIONS.md` outside `## Vault` (after R1) | 2 |
| R6 integration | platform-lead | `docs/REBUILD.md`, `docs/DECISIONS.md`, `docs/STATUS.md`, this folder | 3 |
| V reviews | code-reviewer, security-privacy-reviewer (read-only); findings in `review.md` | — | 4 |

## Acceptance criteria

- **R1** `terraform -chdir=infra/vault test` green with mocks; no `vault_namespace`, no HCP provider, no reviewer
  JWT/Secret/ClusterRoleBinding left; one mount, one role, one policy per service on the in-cluster Vault; the
  committed overlays equal the generator's render for the default services (a test compares them, so they cannot
  drift); the plan from an empty state needs only the cluster, the port-forward, the CA file and
  `TF_VAR_vault_k8s_token`; a tfvars that still sets an HCP variable fails the plan with a message that says what to
  delete; the bootstrap files and their checks fit a Shamir init without OIDC; OPERATIONS → Vault describes the Shamir
  init and per-pod unseal, the consequence of a restart, the generate-root session, and the switch to `awskms`.
- **R2** `terraform test` green in `infra/aws/kubernetes` and `infra/aws/dns`; `vault_seal` defaults to `shamir`
  (no seal stanza, no unseal Pod Identity association) and `awskms` renders today's stanza and association; VSO's peer
  on 8200 is `vault_vso_peer` (default false, opened with the collector's after the init: changed after the security
  review, D207); `admission-values.yaml` admits only the in-cluster address (in the follow-up PR; this PR keeps both); the server's public
  443 egress exists only for `awskms` (if the chart allows it cheaply); DNS defaults are the no-mail set and the Google
  set is one tfvars block away; the expected plan counts from empty (aws, kubernetes) and from the current DNS state are
  stated.
- **R3** the script downloads the chosen state version through the HCP Terraform API, writes the key to a 0600 file,
  prints a fingerprint only, and has a self-test with a fake state; the fallback statement names the exact table and
  column and is idempotent; D93's rotation against the existing database is confirmed from the code (no data change
  beyond `ALTER ROLE … PASSWORD`).
- **R4** no script defaults to HCP; `make k8s-validate` green; nothing names an HCP address outside history and tests
  that pin the old shape.
- **R5** alerts for a sealed pod read as "unseal by hand" under Shamir; `make alerts-check` green; the collector install
  from empty and its token are written down.
- **All** `go -C backend test ./...` only if Go changed (it should not); no secret value in any file; no AI attribution.

## Risks

| Risk | Answer |
| --- | --- |
| A from-empty apply hits an ordering problem the incremental applies never showed (CRDs, webhooks, data sources) | mocked tests can't prove it; each REBUILD step has a plan count and a stop rule; a second plan after a partial apply is allowed only when it shows the remainder |
| Shamir: every Vault pod restart needs a manual unseal; two sealed pods lose the Raft quorum | running workloads keep their synced Secrets (VSO only syncs); `VaultSealed` pages; PDB 2 blocks a second drain until the first pod is unsealed; node maintenance only with the operator at hand; `awskms` is one variable away |
| The old data key is gone | fallback statement; testers re-authorise on their next Sign in with Apple; revocation for older accounts by the user in iOS settings |
| Supabase: new role passwords, network restrictions, SMTP through Google | migrate's D93 rotation sets them; the user checks network restrictions (new NAT addresses) and SMTP before the first sync |
| DNS: DNSSEC at the registrar, cached GoDaddy NS | `dig DS` must be empty before the switch; 24-48 h wait; certificates wait for the delegation |
| The repo is archived | the push fails; the user unarchives first |

## Rollback

Nothing is applied by this PR. Each REBUILD step says how to back out (a failed `infra/aws` apply is destroyed and
re-run; the Vault data is empty until step 7, so a broken init is a wipe of three PVCs and a new init).

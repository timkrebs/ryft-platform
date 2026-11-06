# Vault CE in the cluster: plan (platform-lead)

Status: updated 2026-11-05 night after `design.md` (platform-architect) and `versions.md` (version research) and the
user's four answers. Pins, exact HCL shapes and D-numbers (D186..D196 proposed) come from those two; this file fixes
the order, the owners, the acceptance criteria, the risks and the rollbacks. Where this file and `design.md` differ,
`design.md` wins (it deliberately deviates from the brief in the places listed in its section 13).

**User decisions (2026-11-05):** (1) the Vault server is a **Terraform add-on** in `infra/aws/kubernetes`, not an Argo
CD app; (2) **dual-write** to both Vaults until decommission, instead of freezing HCP; (3) BUSL-1.1 accepted, **Vault
2.2.1** pinned by digest with chart 0.34.1; (4) operator login by **OIDC with Google Workspace** (design recommended
userpass with TOTP: PR 3's bootstrap must therefore specify the OIDC app registration, the Terraform-side config and a
break-glass path through `vault operator generate-root` with the recovery keys, since the login path must not depend on
Google alone during an incident).

**More answers (2026-11-05, night):** the 5 recovery keys are held by the user in 1Password (one custodian: the
threshold of 3 of 5 then protects against loss of a single copy, not against compromise of the vault item; OPERATIONS
should say so and suggest splitting the shares over separate items/people); the three nodes are **ON_DEMAND**
(checked with `kubectl get nodes -L eks.amazonaws.com/capacityType`), so the Spot-interruption wording in the brief
and design becomes node loss or node replacement; Vault's logs (audit device included) go to **Grafana**, not
CloudWatch (needs an addendum: what the audit log holds, where it lands, retention, and a privacy review, because it
is a third-party processor); the **monitoring and import-service groups move to automatic apply** (a separate ticket
V7 before their cutover PRs; the add-on rule in CLAUDE.md and D94 limit what Argo CD may own, so an addendum
decides how); the **EKS public endpoint is narrowed after decommission** (PR 6b or a separate infra PR, the user
applies).

## Ground rules for this run

- This moves every credential RYFT has. **Every PR goes through security-privacy-reviewer**, plus code-reviewer; sre
  reviews PRs 2 and 4 and each cutover PR. A final focused re-read after each fix round (earlier rounds of this
  engagement found real bypasses in every second or third pass).
- Agents prepare, test and document. The user applies every Terraform plan, runs the init, the copy and every cutover
  step. No `terraform apply`, no tfstate, no `terraform.tfvars`, no `kubectl` against production (context
  `ryft-prod-euc1`, always explicit in the commands we hand over).
- No secret value ever appears in a repo file, a PR, a log, a brief or this chat. Tools that compare secrets
  (`scripts/vault_diff.sh`) print key names and `equal`/`differs` only.
- No AI attribution in commits or PRs. D-numbers: next free is D186 (check at merge time; several PRs are in flight).
- Backend image groups are not involved: nothing under `backend/` changes. The staging rule of D175 does not apply
  to these PRs, but each cutover PR is staged by group with its own verification before the next one.
- Decommissioning HCP (PR 6) only after the 14-day soak, and only on the user's explicit go.
- A repo hook blocks a certain brand name in any new text. Refer to that workload as "the import service" and use
  the namespace names the generator emits; do not write the name by hand.

## Tickets (design.md section 12; all PRs: security-privacy-reviewer and code-reviewer, sre on 2, 4 and each cutover)

| PR | Title | Owner | The user |
| --- | --- | --- | --- |
| 1 | `infra: KMS keys and the snapshot bucket for Vault` (`infra/aws`, `prevent_destroy`) | infra-engineer, test-engineer | plan, apply |
| 2a | `admission: the vault namespace` (P1/P2/P3/P4 extended, P13 new, P12 keeps Argo CD out; celtest cases; policies stay disabled) | gitops-engineer | none (ships disabled) |
| 2 | `infra: Vault CE as a Terraform add-on` (`infra/aws/kubernetes`: chart 0.34.1, pod identity roles, dedicated `ryft-vault` CA, local extras chart with Certificate, NetworkPolicies, PDB; kind e2e) | infra-engineer, gitops-engineer, test-engineer | plan, apply; pods 3 Running 0/1 ready |
| 3 | `vault: a Kubernetes target beside HCP` (`infra/vault`, `scripts/vault_diff.sh`, OPERATIONS rewrite incl. OIDC bootstrap and break-glass) | infra-engineer, test-engineer | bootstrap, plan, apply, commit regenerated overlays |
| 3b | `vault: write the in-cluster Vault` (`write_k8s = true`) | infra-engineer | plan, apply through the port-forward; `vault_diff.sh --kv` all equal |
| 4 | `ops: snapshots, metrics and alerts for Vault` | sre, test-engineer | apply, load alert rules, first snapshot check |
| 5a..5i | `vault: cutover of <group>` one PR per group (smoke, notify, scoring, ingest, api, rabbitmq, migrate, monitoring, import service) | gitops-engineer | per group: diff, apply on the branch, merge, verify |
| 6, 6b | `vault: decommission HCP`; `vault: drop the HCP provider and variables` | infra-engineer | explicit go after the soak; apply; delete the HCP cluster |

Order: 1, then 2a and 2 (2 merged after the user's apply of 1), 3, bootstrap, 3b, 4, 5a to 5i one at a time, 14 days
soak, 6, 6b. The tests are built with 2, 3 and 4, not after them. PR 2a must be merged before any cutover PR.

## Acceptance criteria (testable)

V1: `terraform test` for the new resources with mock providers; the plan expectation in the PR body is exactly one KMS
key and alias, one pod identity association, one bucket with policy, versioning, SSE-KMS and a 30-day lifecycle, no
change to anything else; the key policy admits the pod identity role for `Encrypt/Decrypt/DescribeKey` only.

V2: chart and image pinned (version, tag, digest, from `versions.md`); `make k8s-validate` and `admission-celtest`
green on the rendered chart; namespace carries Pod Security `restricted` enforce, audit and warn; default-deny plus
the listed allows; PDB `minAvailable: 2`; `disable_mlock = true`; telemetry; seal `awskms` without static
credentials; Certificate from the internal issuer with the full DNS name list; UI `ClusterIP`; no LoadBalancer; the
kind test passes in CI; no secret in the diff.

V3: for both targets, `terraform test` passes; exactly one mount, one role, one policy per service; `shared_secrets`
land in every `kv-<svc>/config`; the same `random_*` values feed both targets (a test proves one value, two writes);
the regenerated overlays for the Kubernetes target differ from the HCP ones only in `mount`, `address`,
`caCertSecretRef` and the absent `namespace`; `scripts/vault_diff.sh` has a self-test with fake servers and never
prints a value; OPERATIONS covers bootstrap, day 2, snapshots, restore, DR order, "a pod is sealed" and "the leader
is gone".

V4: the CronJob exports a success timestamp; every alert has a promtool test that fires and one that does not;
`make alerts-check` green including the 20-rules-per-group guard (Vault alerts get their own group); the restore into
kind works in CI with a synthetic snapshot.

V5x: before the PR, `scripts/vault_diff.sh` reports zero differences; after the sync, `ryft-secrets` content is
byte-identical (design decides how that is checked without printing values); the workload rolls once; `make
smoke-prod` green; the group's signals clean for 30 minutes.

V6: the plan shows only destroys of HCP objects; docs and the trust drawing updated; the HCP cluster deleted by the
user in the portal.

Done when (brief): every `ryft-secrets` comes from the in-cluster Vault with byte-identical content; `make
smoke-prod` green after each group; a Spot interruption of a Vault node leaves the cluster unsealed and serving; a
snapshot restore into kind succeeds in CI; every alert has a test; HCP is deleted after the soak.

## Risks and how the plan answers them

| Risk | Answer |
| --- | --- |
| Vault becomes a single point of failure on the critical path of secret delivery | Workloads keep their already-mounted Secrets if Vault is down (VSO only syncs); new pods need `ryft-secrets` already present; 3 replicas, PDB 2, anti-affinity; alerts for sealed, no leader and peers; snapshots every 6 h; DR order written and drilled |
| Quorum loss on three Spot nodes | design.md checks the node capacity type and topology; PDB; if nodes are Spot, say so and decide with the user (on-demand node group, or accept the risk with the drill) |
| Auto-unseal depends on KMS and on the pod's identity at start | Pod Identity association created before the chart syncs; KMS permissions limited; a pod that cannot reach KMS stays sealed and `VaultSealed` pages; runbook |
| Bootstrap leaks (recovery keys, root token) | Recovery keys to the password manager only; root token revoked after creating the admin identity and the Terraform token; nothing in chat, git or logs |
| Cutover changes a credential a service reads | Same random values to both targets (copy, not rotation); `vault_diff.sh` before every group; one group at a time; rollback is reverting the overlay PR, HCP still serves |
| A VSO source switch restarts workloads at the wrong time or not at all | design.md settles what restarts (rolloutRestartTargets on a byte-identical source change?) and the kind rehearsal proves it; each cutover PR states the expected roll |
| NetworkPolicy or admission block the new namespace | `admission-celtest` and the kind test run on the rendered manifests; policies listed per flow (VSO to 8200, Raft 8200/8201, KMS 443, S3 443, DNS, metrics) |
| Terraform provider coexistence (HCP and CE in one root) | design.md decides one root or two; tests for both targets; the HCP resources stay untouched until V6 |
| CE license (BUSL) | versions.md summarises; the user accepts or not before V1 merges |
| Vault UI exposure | `ClusterIP` only; port-forward for admin; an Ingress only on the user's request |
| The user's manifest differs from reality (tag `2.2.1` described as Enterprise, replicas 5 vs 3, `global.resources`, static KMS credentials, `LoadBalancer` UI) | Corrected per the review table in the brief; recorded in the V2 PR body |

## Rollbacks

- V1, V2, V4: revert the PR; Argo CD prunes the application (the PVCs are retained by the StatefulSet's default
  policy: say so in the PR body and in OPERATIONS; deleting data is a separate, manual step). KMS key deletion is
  scheduled, not immediate (a 7 to 30 day window).
- V3: nothing is read by workloads until V5; the CE target can be destroyed by the user without effect on HCP.
- V5x: revert the group's overlay PR; VSO resyncs from HCP, which is untouched during the soak.
- V6: not reversible after the HCP cluster is deleted; therefore only after the soak, a fresh `vault_diff.sh` zero,
  a restore drill from the latest snapshot, and the user's explicit go.

## What the user has to run, in order (filled in from design.md)

1. Answer the open questions in `design.md` (license, admin identity, snapshot RPO, recovery key custody, node
   capacity).
2. `infra/aws` plan and apply (V1), after the PR is merged.
3. Merge V2; check the pods (sealed, Running); init and bootstrap (V3 docs).
4. `infra/vault` apply with the Kubernetes target through the port-forward; run `vault_diff.sh`.
5. Load the alert rules (V4); first snapshot and its alert.
   *(Superseded by D194: the first snapshot is taken by hand before the rules are loaded. Kept as the historical plan.)*
6. Cutover groups, with the verify commands in each PR body.
7. Soak 14 days; decommission.

## Update 2026-11-06: reviews of the first three PRs and the addendum

PRs open: **#146** (PR 1, `infra/aws` keys and bucket), **#147** (PR 2a, admission for `vault`), **#145** (PR 2, the
add-on). Security and SRE reviews of #145 found two must-fix items for the user's apply (the injector webhook does not
exclude `vault`; the collector and VSO ingress open 8200 before anything uses it), three SRE items (liveness kills
standbys on a heartbeat gap; the chart PDB blocks every node drain until init; zone spreading is not enforced) and
several smaller ones; they are in a fix round on #145. Reviews of #146 and #147 are running.

**Decisions added by the user (addendum answers):**
- Monitoring group: **V7b and V7c**. V7b is an Argo CD app `vault-monitoring` in a new narrow project for the four VSO
  kinds. V7c turns the collector chart into a Terraform `helm_release` imported from today's release; `make
  monitoring-apply` retires; the user does one state removal by hand (see addendum-1).
- Import service: **V7a**, an Argo CD app for its Vault secret objects (the workload itself is not in git).
- Recovery keys: a **2-2-1 split** (two shares in 1Password, two on paper at home, one on paper off site; a yearly
  generate-root drill).
- Vault logs to Grafana: **accepted as recommended** (a reviewed collector pipeline limited to namespace `vault`, `vault`
  excluded from the remote pipeline on PR 2's day, audit devices file and stdout with `log_raw=false`, OIDC
  `user_claim = "sub"`, 14 days retention; the user accepts Grafana's DPA and updates the record of processing).
- New tickets V7a, V7b, V7c before cutovers 5h and 5i. PR 6c narrows the EKS public endpoint after PR 6 (the operator's
  /32s plus the NAT addresses; private access must be on first). D197 to D205 are proposed in `addendum-1.md`.

### Hard gates before the bootstrap and before PR 3b (`write_k8s = true`), from the security review of #145

These are conditions on the user's steps, not on merging PRs:

1. **G1.** D180 Step 0 (#47) applied, and P12, P13 and the `vault` branches of P1, P2 and P3 **in Deny**. "2a merged" is
   not enough: merged means disabled. Until then an Argo CD controller token can create a Deployment in `vault` with
   serviceAccount `vault` and reach KMS, the auth-delegator binding and the Raft data. The order is therefore: #47
   applied, #121 merged and enabled, #147 merged and enabled, then the bootstrap.
2. **G2.** The monitoring stack sits inside Vault's trust boundary (alloy-daemonset is privileged with hostPID and can
   read the memory of every pod; alloy-operator and VSO have cluster-wide Secrets access). The user decides: accept it in
   D186 and D194 ("Vault is as safe as the monitoring stack and VSO"), or reduce it (drop root, host network and hostPID
   from the DaemonSet, scope alloy-operator, exclude `vault` from Beyla and node-log reading). The Art. 32 description
   must not claim Vault is isolated from the monitoring vendor.
3. **G3.** Never `vault login` through `kubectl exec` (it writes a token file to a disk emptyDir); use the port-forward
   with `-no-store` or `VAULT_TOKEN`. In PR 3's runbook. If `vault status` already shows `Initialized true` before the
   user's own init, stop, treat it as a compromise and wipe the PVCs and EBS volumes.
4. **G4.** `prevent_destroy` on the namespace and both Helm releases (in the #145 fix round).

Cutover PRs 5x do not start before G1 to G4 hold.

---
name: project-vault-ce-rollout
description: State and ownership of the in-cluster Vault CE migration (PR order, who owns which files, what ships off) as of 2026-11-06
metadata:
  type: project
---

Vault CE replaces HCP Vault in ryft-prod-euc1. PR order: #146 (infra/aws keys + snapshot bucket),
#145 (Vault add-on in infra/aws/kubernetes), #147 (admission chart 0.5.0 for namespace vault),
PR 3 (infra/vault Kubernetes target, another agent, branch infra/vault-k8s-target; it owns
OPERATIONS bootstrap/day-2 and generates the per-namespace vault-ca Certificates), PR 4 (SRE:
snapshot CronJob, scrapes, group ryft-vault, log pipeline; branch ops/vault-snapshots-alerts).
Admission is `enabled: false` in admission-values.yaml.

**Why:** the D186-D198 decision numbers used in comments come from the design work docs
(docs/work/2026-11-05-vault-ce, untracked, gone from disk by 2026-11-06), not from DECISIONS.md;
the lead writes the entries.

**How to apply:** before touching Vault ops, check which of PR 3/PR 4 merged (`gh pr list`). The
Vault log pipeline (`podLogsViaLoki`) ships off until LOKI_USERNAME is in ryft-secrets and the user
accepted Grafana's DPA; the Vault metrics scrape needs monitoring/vault-ca (from PR 3). Never
SSH/kubectl-change prod: hand the user commands with `--context ryft-prod-euc1`.
Related: [[feedback-worktree-hooks]].

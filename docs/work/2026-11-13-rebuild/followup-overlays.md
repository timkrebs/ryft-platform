# Follow-up PR: the regenerated Vault overlays (after REBUILD step 7, before step 8)

The rebuild PR leaves `deploy/k8s/overlays/vault-namespaces/*/` as they are on `main`: HCP's render (address
`https://ryft-prod-euc1-vault-public-vault-d9cda245.5ea5b8ce.z1.hashicorp.cloud:8200`, `namespace: admin/ryft/<svc>`,
mount `kv`, no `Certificate vault-ca`). Only the user's `infra/vault` apply (step 7) writes the in-cluster render, and
no one edits these files by hand. So the rebuild PR keeps everything that reads them green against HCP's shape, and
this follow-up flips it in the same commit as the regenerated files. It must merge before Argo CD's first sync
(step 8): until then Argo CD would apply HCP-shaped VaultConnections that point at a deleted Vault.

Nothing in this PR changes a Terraform plan: the admission chart is disabled (`enabled: false`, D210), so
`vaultAddresses` feeds no release, and the generated files are already in `infra/vault`'s state after step 7.

## 1. Collect what the apply wrote

In the checkout of `main` the apply ran from (the rewritten files go with the new branch):

```bash
git switch -c deploy/vault-overlays-in-cluster
git status --short deploy/k8s/overlays/vault-namespaces      # nine vault.yaml rewritten, kustomization.yaml unchanged
grep -h 'address:' deploy/k8s/overlays/vault-namespaces/*/vault.yaml | sort -u   # only https://vault.vault.svc:8200
```

## 2. `infra/vault`: hold the committed overlays to the generator again

### 2a. Add this run to `infra/vault/tests/vault.tftest.hcl`, right after `operator_manifests_match_the_goldens`

The run as it was written, verbatim:

```hcl
# The committed overlays are what Argo CD applies (vault-<svc>) and what the first apply from an empty state writes
# again: byte for byte the render of the default services, one directory per service and none besides. So a PR
# cannot ship files an apply would rewrite, or leave a removed service's behind. A change of the template, the
# services or a service's key set changes them: commit what the apply wrote, in the same PR.
run "the_committed_overlays_are_what_the_apply_writes" {
  command = plan

  assert {
    condition     = alltrue([for k, f in local_file.vso : try(file(f.filename), "MISSING") == f.content])
    error_message = "deploy/k8s/overlays/vault-namespaces/<svc>/vault.yaml is missing or is not the render of templates/vso.yaml.tftpl for: ${join(", ", [for k, f in local_file.vso : k if try(file(f.filename), "MISSING") != f.content])}. Commit what the apply writes."
  }
  assert {
    condition     = alltrue([for k, f in local_file.vso_kustomization : try(file(f.filename), "MISSING") == f.content])
    error_message = "deploy/k8s/overlays/vault-namespaces/<svc>/kustomization.yaml is missing or is not what vso.tf writes for: ${join(", ", [for k, f in local_file.vso_kustomization : k if try(file(f.filename), "MISSING") != f.content])}."
  }
  assert {
    condition = (
      fileset(var.vso_output_dir, "*/vault.yaml") == toset([for k in keys(var.services) : "${k}/vault.yaml"]) &&
      fileset(var.vso_output_dir, "*/kustomization.yaml") == toset([for k in keys(var.services) : "${k}/kustomization.yaml"])
    )
    error_message = "deploy/k8s/overlays/vault-namespaces has a directory for a name that is no service (a removed service's files?), or lacks one"
  }
}
```

One addition is required: the rebuild PR sets `vso_output_dir = "tests/.vso-render"` in the file's top `variables`
block (so no run with `command = apply` can ever write into, or tear down, the real overlays). This run only plans and
must read the real directory, so give it its own value, directly under `command = plan`:

```hcl
  # Plan only: reads the committed overlays, writes nothing (the file-level value keeps every apply away from them).
  variables {
    vso_output_dir = "../../deploy/k8s/overlays/vault-namespaces"
  }
```

No other run may read the committed overlays.

### 2b. Comments that describe the state (text only)

- `infra/vault/tests/vault.tftest.hcl`, header: replace the sentences from "No run reads the committed ..." to
  "... (docs/work/2026-11-13-rebuild/followup-overlays.md)." with: "The committed
  deploy/k8s/overlays/vault-namespaces/ must be exactly what an apply writes
  (the_committed_overlays_are_what_the_apply_writes, the only run that reads them, plan only)."; and in the first lines
  "the goldens and the bootstrap files" becomes "the goldens, the committed overlays, the bootstrap files".
- `infra/vault/vso.tf`, header: the sentence about HCP's render becomes "... against tests/golden/k8s/ and, for the
  default services, against the committed deploy/k8s/overlays/vault-namespaces/ (tests/vault.tftest.hcl), so a PR
  cannot ship files an apply would rewrite."
- `infra/vault/README.md` -> "The generated overlays": drop the two sentences about HCP's render and the follow-up;
  keep "`tests/vault.tftest.hcl` pins the committed files to exactly what an apply writes for the default services
  (`the_committed_overlays_are_what_the_apply_writes`) and three of them to `tests/golden/k8s/`."

## 3. `infra/aws/kubernetes`: the in-cluster address only

### 3a. `admission-values.yaml`

```yaml
# The Vault addresses P4 admits on a VaultConnection: the in-cluster Vault's only, the only
# Vault since the rebuild (D207; HCP Vault is deleted). A check and a terraform test hold every
# VaultConnection infra/vault generates (deploy/k8s/overlays/vault-namespaces/*/vault.yaml)
# to this list.
vaultAddresses:
  - https://vault.vault.svc:8200
```

### 3b. `admission.tf`, the comment above `vault_namespaces_dir`, and `infra/aws/README.md`

```hcl
  # vaultAddresses (the in-cluster Vault's only, D207); a check and a test.
```

`infra/aws/README.md` -> "It ships off": the parenthesis after `vaultAddresses` becomes "(only the in-cluster
`https://vault.vault.svc:8200`, D207; a check requires every generated
`deploy/k8s/overlays/vault-namespaces/*/vault.yaml` to carry it)", and "the Vault addresses" becomes "the Vault address".

### 3c. `tests/admission.tftest.hcl`

In `vault_address_matches_the_generated_vault_yaml`, replace the comment and the assert that pins
`[HCP's address, "https://vault.vault.svc:8200"]` with these two:

```hcl
  assert {
    condition     = jsonencode(local.admission_file.vaultAddresses) == jsonencode(["https://vault.vault.svc:8200"])
    error_message = "vaultAddresses holds the in-cluster Vault's address only (https://vault.vault.svc:8200): it is the only Vault since the rebuild (D207), HCP Vault is deleted"
  }

  # Every committed VaultConnection names the in-cluster Vault.
  assert {
    condition     = jsonencode(local.vault_addresses) == jsonencode(["https://vault.vault.svc:8200"])
    error_message = "a generated deploy/k8s/overlays/vault-namespaces/*/vault.yaml names another Vault than https://vault.vault.svc:8200: regenerate the overlays with infra/vault (D207)"
  }
```

The comment above `vault_address_check_admits_the_in_cluster_vault` becomes:

```hcl
# Only the in-cluster Vault: a tree where every namespace names it passes the check; a namespace left
# on HCP's address, an address in no list, or a file with two addresses fails it.
```

Replace the run `vault_address_check_admits_hcp_until_the_follow_up` (and its three comment lines) with:

```hcl
run "vault_address_check_refuses_a_namespace_left_on_hcp" {
  command = plan

  variables {
    admission_vault_namespaces_dir = "tests/fixtures/vault-namespaces-cutover"
  }

  assert {
    condition     = !local.admission_vault_address_ok && length(local.vault_addresses) == 2
    error_message = "a tree with one namespace still on HCP Vault's address must fail check.admission_vault_address (HCP is no longer in vaultAddresses)"
  }

  expect_failures = [check.admission_vault_address]
}
```

`vault_address_check_admits_the_in_cluster_vault`, `..._refuses_a_foreign_address`, `..._refuses_two_addresses_in_one_file`
and `..._refuses_an_empty_tree` stay as they are.

### 3d. Fixture comment (`tests/fixtures/vault-namespaces-cutover/notify/vault.yaml`, line 1)

```yaml
# Test input of admission.tftest.hcl: a namespace left on HCP Vault (deleted; refused since D207).
```

### 3e. The CEL harness (`admission-celtest/`)

`cases_vault.go`, in `runVault`: replace the two comment lines and the admitted case
`"P4 the HCP address with caCertSecretRef vault-ca (admitted until the follow-up)"` with:

```go
	add("P4 the HCP address is refused (the in-cluster Vault only, D207)", argoCtl, "CREATE", set(vc(), "spec/address", hcpAddress), "P4: a VaultConnection")
```

The comment on `const hcpAddress` becomes: "The generated VaultConnections name the in-cluster Vault now (D207), so the
cases that refuse HCP's address carry it as a constant instead of reading it from the overlays."

`testdata/messages.golden`: between the `vault: P4 kv-api read from ryft-notify` entry and `vault: P4 the active pod's
address`, add:

```
vault: P4 the HCP address is refused (the in-cluster Vault only, D207)
    ryft-vso: P4: a VaultConnection must carry a RYFT Vault address, verify TLS, take a CA only from the Secret vault-ca, and set no header or timeout.
```

Then run the harness. Several cases build their objects from the committed overlays (`main.go`, the P4 negatives
around `find("VaultConnection", "ryft-api", "vault")`; `cases_hardening.go`, the VaultAuth and VaultStaticSecret
details), and the regenerated files add one `Certificate vault-ca` per namespace to the positives. If the golden
differs anywhere else, read the diff first; only an expected change is rewritten with `UPDATE_GOLDEN=1`.

```bash
make admission-celtest
```

### 3f. The kind run's fixtures (CI diffs them against the harness's output)

`ryft-vso-01` to `ryft-vso-04` in `admission-e2e/fixtures/generated/` are built from the committed overlays (today
they carry HCP's address and `admin/ryft/api`), so they change with them. Regenerate, from the repository root:

```bash
CELTEST_EMIT=../admission-e2e/fixtures/generated infra/aws/kubernetes/admission-celtest/run.sh
git status --short infra/aws/kubernetes/admission-e2e/fixtures/generated   # expect the ryft-vso-* files
```

(The path is relative to `admission-celtest/`, where `run.sh` runs the harness.)

## 4. Verify, then commit

```bash
terraform -chdir=infra/vault fmt -check -recursive && terraform -chdir=infra/vault init -backend=false && \
  terraform -chdir=infra/vault validate && terraform -chdir=infra/vault test
terraform -chdir=infra/aws/kubernetes fmt -check -recursive && terraform -chdir=infra/aws/kubernetes init -backend=false && \
  terraform -chdir=infra/aws/kubernetes validate && terraform -chdir=infra/aws/kubernetes test
make admission-celtest
make k8s-validate
bash scripts/tests/admission_values_test.sh
git add deploy/k8s/overlays/vault-namespaces infra/vault infra/aws/kubernetes
git commit -m "deploy: Vault overlays on the in-cluster Vault, HCP's address out of P4"
```

`terraform test` in `infra/vault` also reads the local `terraform.tfvars`: delete its HCP lines
(`vault_parent_namespace_root`, `vault_address_for_cluster`) first, or every run fails on the retired variables.

CI's `admission` job then dry-runs the regenerated overlays on kind in Audit and Deny, against the committed values.

Not part of this follow-up: P4's remaining HCP fields (`vso.kvMount`, `vso.vaultNamespacePrefix`, the cases "still
admitted until the decommission" and the `decom:` render in `run.sh`). They go with the admission chart's later
Audit/Deny steps (D210).

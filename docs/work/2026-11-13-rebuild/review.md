# Review of the rebuild PR (#161)

Read-only reviews by code-reviewer and security-privacy-reviewer on the first push (0f9044f), plus the red CI of that
push. What was found, and what became of it.

## CI on 0f9044f
- `terraform (infra/vault)`: asserts that read only `file("k8s.tf")` and similar referred to no object. Fixed: every
  file read goes through `path.module`.
- `smoke` (`make test-scripts`): `kind_guard_test.sh` failed with `KUBECONFIG: unbound variable`. The ubuntu-26.04
  runners' bash no longer keeps a function's prefix assignment after it returns. Not the PR's code, but it blocks CI.
  Fixed in the test: the variables are exported inside the subshell.

## Security (blocker and should-fix)
| Finding | Outcome |
| --- | --- |
| **G1 gap (blocker):** without admission in Deny, a pod with Vault's labels in `vault` joins `svc/vault-active` and takes shares and tokens | Fixed in procedure: every share and token goes only to a verified `pod/vault-N` (StatefulSet uid, pinned digest, `vault-active`), checked again after the forward (OPERATIONS → Vault → A verified pod). D210 records it as blocking in G1's place. G1 itself stays a follow-up, now recommended before `awskms` and soon after the testers return |
| VSO's peer open from the first apply, during the unauthenticated init window | Fixed: `vault_vso_peer` (default false), opened with the collector's in Bootstrap step 11 after root is revoked; off again for DR and a lost volume (D207) |
| The admin policy lets the admin token mint orphans or rewrite policies | Fixed: denies on token creation except `create/terraform`, on token roles and `sys/auth/*`; policies read-only (D209) |
| Clipboard: Handoff and clipboard managers keep shares | Fixed: OPERATIONS → Vault → The clipboard, REBUILD "Before you start" |
| The signed state URL in curl's argv | Fixed: every URL in curl's stdin config (D211) |
| Time Machine keeps `~/.config/ryft` | Fixed: `tmutil addexclusion` before the write; a local snapshot taken meanwhile still keeps it (stated) |
| A 404 read as "the key is lost" | Fixed: the message says gone or hidden, and the user checks the HCP UI first |
| No `prevent_destroy` on the data key | Fixed, with a test (not on the demo password: `-replace` rotates it) |
| The import puts the value in argv (`ps`) | Kept, stated plainly in OPERATIONS → The first apply (one process, run nothing else meanwhile); an `import` block would put it in a file instead |
| D208 wording on Shamir's protection | Fixed: any path to 8200; Shamir does not protect a running Vault from node root or hostPID |
| The privacy text names `support@ryftapp.de` while no mail works | Flagged to the owner (D212, STATUS); no web change in this PR |

## Code review (should-fix)
| Finding | Outcome |
| --- | --- |
| `[default api]` printed as the login check's result | Fixed: `[api default]` (Vault sorts) in REBUILD, OPERATIONS and the README |
| No `terraform init` before the imports | Fixed in OPERATIONS and REBUILD step 7 |
| The import re-run with stderr shown could print the value | Fixed: stderr filtered through `grep -vFf` |
| The 90 count's assumptions | Stated (the import service's and monitoring's keys set, none for `rabbitmq`) |
| `vault_extras_chart_test.sh` renders only the default | Fixed: a `kmsSeal=false` render, no `0.0.0.0/0` and no Pod Identity egress |
| STATUS "D206 is next" | Fixed |
| `svc/vault-active` in the helper scripts' hints (found while integrating, not by a reviewer) | Fixed: the verified pod (`admin.sh` and its test, `seed_demo.sh`, `vault_diff.sh`, the output's description) |

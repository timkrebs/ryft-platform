#!/usr/bin/env bash
# The lifecycle conditions of helm_release.argocd (infra/aws/kubernetes/argocd.tf, T12-f): the release takes
# its values from local.argocd_values only, so the values guard and the tests see what Helm gets. A `set`
# (B1) or a second values document (B2) reaches Helm without passing through the guard; neither can be
# injected from a test file (override_resource does not replace configured arguments), so this copies the
# module, edits the release, and runs tests/addons.tftest.hcl (mock providers, nothing is created) on:
#   - the copy as it is: passes;
#   - a `set` of createClusterRoles=false and configs.rbac.policy.default: fails on the postcondition;
#   - a second values document with a wildcard controller role: fails on the postcondition;
#   - a value the guard module refuses (createAggregateRoles): fails on the precondition.
# Needs terraform and the providers (a plugin cache, or the network, for `init`); without terraform the
# test is skipped, and without TF_PLUGIN_CACHE_DIR. Part of make test-scripts (where it usually skips) and
# run by the terraform CI job for infra/aws/kubernetes.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
MOD=infra/aws/kubernetes
fail() { echo "✗ argocd_release_guard_test: $*" >&2; exit 1; }
if ! command -v terraform >/dev/null 2>&1; then echo "✓ argocd_release_guard_test (skipped: no terraform)"; exit 0; fi
# Four `init`s download the providers unless a plugin cache holds them: run it where one is set (the CI job
# for infra/aws/kubernetes sets TF_PLUGIN_CACHE_DIR; locally `TF_PLUGIN_CACHE_DIR=~/.terraform.d/plugin-cache`).
if [ -z "${TF_PLUGIN_CACHE_DIR:-}" ]; then echo "✓ argocd_release_guard_test (skipped: TF_PLUGIN_CACHE_DIR is not set)"; exit 0; fi
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT

EDIT=$tmp/edit.py
cat > "$EDIT" <<'PY'
import sys
name, path = sys.argv[1], sys.argv[2]
s = open(path).read()
VALUES = '  values = [yamlencode(local.argocd_values)]\n'
DEX = '    dex           = { enabled = false }\n'
if name == "b1-set":
    old, new = VALUES, VALUES + '  set = [{ name = "createClusterRoles", value = "false" }, { name = "configs.rbac.policy\\\\.default", value = "role:admin" }]\n'
elif name == "b2-second-document":
    wild = 'controller:\\n  clusterRoleRules:\\n    enabled: true\\n    rules:\\n      - apiGroups: [\\"*\\"]\\n        resources: [\\"*\\"]\\n        verbs: [\\"*\\"]\\n'
    old, new = VALUES, '  values = [yamlencode(local.argocd_values), "%s"]\n' % wild
elif name == "b3-guard-violation":
    old, new = DEX, DEX + '    createAggregateRoles = true\n'
else:
    sys.exit("unknown variant " + name)
if old not in s:
    sys.exit("anchor not found for " + name)
open(path, "w").write(s.replace(old, new, 1))
PY

# variant <name> <expected text of the failure, or "" for a pass>
variant() {
  local name=$1 want=$2 root="$tmp/$1" rc=0
  local d="$root/$MOD"
  mkdir -p "$d"
  cp -R "$HERE/deploy" "$root/deploy"   # the module reads ../../../deploy (the Vault addresses, the operators' RBAC)
  cp -R "$HERE/$MOD/." "$d/"
  rm -rf "$d/.terraform"
  if [ "$name" != clean ]; then python3 -I "$EDIT" "$name" "$d/argocd.tf" || fail "$name: the edit did not apply (argocd.tf changed shape?)"; fi
  (cd "$d" && terraform init -backend=false -input=false -no-color >"$d/init.log" 2>&1) || { cat "$d/init.log" >&2; fail "$name: init failed"; }
  (cd "$d" && terraform test -no-color -filter=tests/addons.tftest.hcl >"$d/test.log" 2>&1) || rc=$?
  if [ -z "$want" ]; then
    [ "$rc" = 0 ] || { tail -30 "$d/test.log" >&2; fail "$name: expected the tests to pass, rc=$rc"; }
  else
    [ "$rc" != 0 ] || fail "$name: expected the tests to FAIL ($want), they passed"
    grep -q "$want" "$d/test.log" || { tail -30 "$d/test.log" >&2; fail "$name: failed, but not with '$want'"; }
  fi
}

variant clean ""
variant b1-set "the release takes its values from local.argocd_values only"
variant b2-second-document "the release takes its values from local.argocd_values only"
variant b3-guard-violation "the Argo CD values carry something they must not"

echo "✓ argocd_release_guard_test"

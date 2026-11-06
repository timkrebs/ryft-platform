#!/usr/bin/env bash
# The lifecycle conditions of helm_release.argocd (infra/aws/kubernetes/argocd.tf, T12-f, D180): the release takes
# the values document that the guard module checked (module.argocd_values_guard.values_yaml) and nothing
# else, so what the guard and the tests see is what Helm gets. None of these can be injected from a test
# file (override_resource does not replace configured arguments), so this copies the module, edits it, and
# runs tests/addons.tftest.hcl (mock providers, nothing is created) on:
#   - the copy as it is: passes;
#   - b1: a `set` of createClusterRoles=false and configs.rbac.policy.default: fails on the postcondition;
#   - b2: a second values document with a wildcard controller role: fails on the postcondition;
#   - a: the reviewed map renamed and fed to the guard, argocd_values redefined as merge(base, {...}) and
#     still used by the release: fails on the postcondition (the guard saw the base, Helm would get the merge);
#   - b: the release's values wrapped in a merge of its own: fails on the postcondition;
#   - b3: a value the guard module refuses (createAggregateRoles): fails on the precondition.
# Every edit asserts its anchors, so a variant fails for the reason named, never for 'anchor not found'.
# What it cannot see: a change to the guard module together with this script's anchors in one PR; that is
# caught only by review (.github/CODEOWNERS: everything is owned by @timkrebs).
# Needs terraform and a plugin cache (four `init`s download the providers otherwise). Locally it skips
# without them; in CI's terraform job (GITHUB_JOB=terraform) or with ARGOCD_RELEASE_GUARD_REQUIRED=1 it
# FAILS without them. Run by that job for infra/aws/kubernetes.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
MOD=infra/aws/kubernetes
fail() { echo "✗ argocd_release_guard_test: $*" >&2; exit 1; }
# Required (a skip is a failure) in CI's terraform job, where this is the proof of the release's conditions,
# and wherever ARGOCD_RELEASE_GUARD_REQUIRED is set. The CI job that runs `make test-scripts` has no
# terraform and is not that job: it skips.
skip() {
  if [ -n "${ARGOCD_RELEASE_GUARD_REQUIRED:-}" ] || [ "${GITHUB_JOB:-}" = terraform ]; then fail "$1, and this run must not skip"; fi
  echo "✓ argocd_release_guard_test (skipped: $1)"; exit 0
}
command -v terraform >/dev/null 2>&1 || skip "no terraform"
# Locally: `TF_PLUGIN_CACHE_DIR=~/.terraform.d/plugin-cache` (the CI job for infra/aws/kubernetes sets it).
[ -n "${TF_PLUGIN_CACHE_DIR:-}" ] || skip "TF_PLUGIN_CACHE_DIR is not set"
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT

EDIT=$tmp/edit.py
cat > "$EDIT" <<'PY'
import sys
name, path = sys.argv[1], sys.argv[2]
s = open(path).read()


def sub(old, new):
    global s
    if s.count(old) != 1:
        sys.exit("anchor not found exactly once: %r (argocd.tf changed shape?)" % old)
    s = s.replace(old, new, 1)


VALUES = '  values = [module.argocd_values_guard.values_yaml]\n'
DEX = '    dex           = { enabled = false }\n'
MAP = '  argocd_values = {\n'
CALL = '  values = local.argocd_values\n}\n'
if name == "b1-set":
    sub(VALUES, VALUES + '  set = [{ name = "createClusterRoles", value = "false" }, { name = "configs.rbac.policy\\\\.default", value = "role:admin" }]\n')
elif name == "b2-second-document":
    wild = 'controller:\\n  clusterRoleRules:\\n    enabled: true\\n    rules:\\n      - apiGroups: [\\"*\\"]\\n        resources: [\\"*\\"]\\n        verbs: [\\"*\\"]\\n'
    sub(VALUES, '  values = [module.argocd_values_guard.values_yaml, "%s"]\n' % wild)
elif name == "a-renamed-base":
    sub(MAP, '  argocd_values_base = {\n')
    sub(CALL, '  values = local.argocd_values_base\n}\n\nlocals {\n  argocd_values = merge(local.argocd_values_base, { createAggregateRoles = true })\n}\n')
    sub(VALUES, '  values = [yamlencode(local.argocd_values)]\n')
elif name == "b-merge-in-release":
    sub(VALUES, '  values = [yamlencode(merge(local.argocd_values, { createAggregateRoles = true }))]\n')
elif name == "b3-guard-violation":
    sub(DEX, DEX + '    createAggregateRoles = true\n')
else:
    sys.exit("unknown variant " + name)
open(path, "w").write(s)
PY

# variant <name> <expected text of the failure, or "" for a pass>
variant() {
  local name=$1 want=$2 root="$tmp/$1" rc=0
  local d="$root/$MOD"
  mkdir -p "$d"
  cp -R "$HERE/deploy" "$root/deploy"   # the module reads ../../../deploy (the Vault addresses, the operators' RBAC)
  cp -R "$HERE/$MOD/." "$d/"
  rm -rf "$d/.terraform"
  if [ "$name" != clean ]; then python3 -I "$EDIT" "$name" "$d/argocd.tf" || fail "$name: the edit did not apply"; fi
  (cd "$d" && terraform init -backend=false -input=false -no-color >"$d/init.log" 2>&1) || { cat "$d/init.log" >&2; fail "$name: init failed"; }
  (cd "$d" && terraform test -no-color -filter=tests/addons.tftest.hcl >"$d/test.log" 2>&1) || rc=$?
  if [ -z "$want" ]; then
    [ "$rc" = 0 ] || { tail -30 "$d/test.log" >&2; fail "$name: expected the tests to pass, rc=$rc"; }
  else
    [ "$rc" != 0 ] || fail "$name: expected the tests to FAIL ($want), they passed"
    grep -q "$want" "$d/test.log" || { tail -30 "$d/test.log" >&2; fail "$name: failed, but not with '$want'"; }
  fi
}

POST="the release takes the checked values document of the guard module and nothing"
variant clean ""
variant b1-set "$POST"
variant b2-second-document "$POST"
variant a-renamed-base "$POST"
variant b-merge-in-release "$POST"
variant b3-guard-violation "the Argo CD values carry something they must not"

echo "✓ argocd_release_guard_test"

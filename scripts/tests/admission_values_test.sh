#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2034
# The checks of values that come from the tree under test (scripts/lib/admission_lib.sh: adm_valid_*): a
# label key such as --kubeconfig, a name with a path or a blank, a path that leaves the tree are refused;
# ordinary names, paths and labels pass. No cluster is contacted.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ admission_values_test: $*" >&2; exit 1; }
ROOT=$HERE WORK=$tmp REPO=$tmp/tree MODE=ci
export ROOT WORK REPO MODE
. "$HERE/scripts/lib/admission_lib.sh"
ok() { "$@" || fail "expected to pass: $*"; }
no() { if "$@"; then fail "expected a refusal: $*"; fi; }

for n in api ryft-api vault-notify a a1 x-y-z; do ok adm_valid_name "$n"; done
for n in "" -x --kubeconfig '--context=prod' 'a b' 'a/b' '../x' 'A' 'a_b' 'a.b' '-' 'a-' "$(printf 'a%.0s' $(seq 1 64))" $'a\nb' ' a'; do no adm_valid_name "$n"; done

for p in deploy/k8s/overlays/eks deploy/rabbitmq deploy/k8s/overlays/vault-namespaces/api a.b/c-d_e; do ok adm_valid_path "$p"; done
for p in "" /etc deploy/../../x ../x deploy//x 'deploy/ x' 'deploy/x/' '--server=x' $'deploy/x\ny'; do no adm_valid_path "$p"; done

for k in app.kubernetes.io/part-of ryft.dev/service a A-b_c.d example.com/Key-1; do ok adm_valid_label_key "$k"; done
for k in "" --kubeconfig --context=prod -s '--as' 'a b' '/x' 'a/' 'a/b/c' '.x' 'x.' 'a=b' $'a\nb' "$(printf 'a%.0s' $(seq 1 64))"; do no adm_valid_label_key "$k"; done

for v in "" x api ryft-api 1.2.3 a_b.c-d; do ok adm_valid_label_value "$v"; done
for v in --kubeconfig=/x -x ' x' 'a b' 'a=b' $'a\nb' '.x' "$(printf 'a%.0s' $(seq 1 64))"; do no adm_valid_label_value "$v"; done

# adm_create_namespaces writes the namespaces, their labels and the service accounts into YAML from the
# tree's directory names, `namespace:` and VaultAuth: every one must be a DNS name before it is written
# (kc and kubectl are stubs; yq is mikefarah's, as in the workflow).
if yq --version 2>&1 | grep -q mikefarah; then
  mkdir -p "$WORK/log"; adm_init; say() { :; }
  kc() { echo "kc $*" >> "$tmp/kc.log"; }
  kubectl() { :; }
  mktree() { # <dir name> <namespace> <serviceAccount> <VaultAuth namespace>
    rm -rf "$REPO"; mkdir -p "$REPO/deploy/k8s/overlays/vault-namespaces/$1" "$REPO/deploy/k8s/services/web"
    printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ryft-web\n' > "$REPO/deploy/k8s/services/web/namespace.yaml"
    printf 'namespace: "%s"\n' "$2" > "$REPO/deploy/k8s/overlays/vault-namespaces/$1/kustomization.yaml"
    printf 'kind: VaultAuth\nmetadata:\n  name: x\n  namespace: "%s"\nspec:\n  kubernetes:\n    serviceAccount: "%s"\n' "$4" "$3" > "$REPO/deploy/k8s/overlays/vault-namespaces/$1/vault.yaml"
    : > "$tmp/kc.log"; : > "$RESULTS"
  }
  mktree api ryft-api api ryft-api
  adm_create_namespaces || fail "a good tree was refused"
  grep -q 'kc apply' "$tmp/kc.log" || fail "a good tree was not applied"
  grep -q 'name: api' "$WORK/namespaces.yaml" && grep -q 'ryft.dev/vault-namespace: admin_ryft_api' "$WORK/namespaces.yaml" || fail "the namespace YAML lost its content"
  bad() { # <what> <dir> <ns> <sa> <vaultauth ns>
    mktree "$2" "$3" "$4" "$5"
    if adm_create_namespaces; then fail "$1: expected a refusal"; fi
    grep -q 'namespaces	FAIL' "$RESULTS" || fail "$1: no FAIL recorded"
    [ ! -s "$tmp/kc.log" ] || fail "$1: something was applied: $(cat "$tmp/kc.log")"
  }
  bad "a directory name with YAML in it" 'api: {x: 1}' ryft-api api ryft-api
  bad "a namespace with a second key" api $'ryft-api\n    evil: 1' api ryft-api
  bad "a namespace with a colon" api 'ryft-api: x' api ryft-api
  bad "an upper-case namespace" api RYFT api ryft-api
  bad "a service account with YAML in it" api ryft-api 'api, namespace: other' ryft-api
  bad "a service account with a blank" api ryft-api 'a b' ryft-api
  bad "a VaultAuth namespace with a blank" api ryft-api api 'ryft-api x'
  bad "a VaultAuth namespace with a flow mapping" api ryft-api api 'x }, {y: 1'
  unset -f kc kubectl
fi

echo "✓ admission_values_test"

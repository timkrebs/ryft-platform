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

echo "✓ admission_values_test"

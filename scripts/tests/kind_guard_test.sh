#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2329
# scripts/lib/kind_guard.sh on fake kubeconfigs (make test-scripts): only a kind cluster's own
# single-context file with a server exactly on 127.0.0.1 passes; the wrappers refuse arguments that
# would choose another cluster. Reads files with `kubectl config`; no cluster is contacted.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
if ! command -v kubectl >/dev/null || ! command -v jq >/dev/null; then echo "✓ kind_guard_test (skipped: no kubectl or jq)"; exit 0; fi
fail() { echo "✗ kind_guard_test: $*" >&2; exit 1; }
# shellcheck source=../lib/kind_guard.sh
. "$HERE/scripts/lib/kind_guard.sh"

mk() { # <file> <context> <server>: a kubeconfig with one context
  local f=$1
  KUBECONFIG="$f" kubectl config set-cluster "c-$2" --server "$3" >/dev/null
  KUBECONFIG="$f" kubectl config set-credentials "u-$2" --token x >/dev/null
  KUBECONFIG="$f" kubectl config set-context "$2" --cluster "c-$2" --user "u-$2" >/dev/null
  KUBECONFIG="$f" kubectl config use-context "$2" >/dev/null
}
ok() { ( KUBECONFIG="$1" HOME="${2:-$tmp/home}" kind_guard ) >/dev/null 2>&1 || fail "expected the guard to pass: $3"; }
no() { if ( KUBECONFIG="$1" HOME="${2:-$tmp/home}" kind_guard ) >/dev/null 2>&1; then fail "expected the guard to refuse: $3"; fi; }

mkdir -p "$tmp/home/.kube" "$tmp/k"
mk "$tmp/k/kind-ok" kind-adm https://127.0.0.1:41823
mk "$tmp/k/kind-v6" kind-adm 'https://[::1]:6443'
ok "$tmp/k/kind-ok" "" "a kind context on 127.0.0.1"
ok "$tmp/k/kind-v6" "" "a kind context on ::1"

# The server is anchored: nothing but https://127.0.0.1:<port> (or [::1]).
i=0
for bad in https://localhost:6443 'https://localhost:' 'https://127.0.0.1:1@remote.example.com' 'https://127.0.0.1@evil.example.com:6443' \
  'https://127.0.0.1:' 'https://127.0.0.1' 'https://127.0.0.1:6443/proxy' 'https://127.0.0.1:6443@10.0.0.5' 'http://127.0.0.1:6443' \
  'https://127.0.0.1.example.com:6443' 'https://user:pw@127.0.0.1:6443' 'https://127.0.0.1:99999999' 'https://[::1]:' 'https://[::1]:1@x' \
  'https://0.0.0.0:6443' 'https://10.0.0.5:6443' 'https://127.0.0.2:6443'; do
  i=$((i + 1))
  mk "$tmp/k/bad$i" kind-adm "$bad"
  no "$tmp/k/bad$i" "" "the server '$bad'"
done

mk "$tmp/k/prod" arn:aws:eks:eu-central-1:123456789012:cluster/ryft https://ABC.gr7.eu-central-1.eks.amazonaws.com
no "$tmp/k/prod" "" "an EKS context"
mk "$tmp/k/kind-remote" kind-adm https://10.0.0.5:6443
no "$tmp/k/kind-remote" "" "a kind-named context on a remote server"
mk "$tmp/k/other" minikube https://127.0.0.1:8443
no "$tmp/k/other" "" "a local context that is not kind-*"
no "$tmp/k/missing" "" "a file that does not exist"
no "$tmp/k/kind-ok:$tmp/k/prod" "" "a list of files"
if ( unset KUBECONFIG; HOME="$tmp/home" kind_guard ) >/dev/null 2>&1; then fail "expected the guard to refuse an unset KUBECONFIG"; fi

# A proxy, or a user that runs a program: refused.
cp "$tmp/k/kind-ok" "$tmp/k/proxy"
KUBECONFIG="$tmp/k/proxy" kubectl config set-cluster c-kind-adm --proxy-url http://proxy.example.com:3128 >/dev/null
no "$tmp/k/proxy" "" "a cluster with proxy-url"
# (kubectl reads a JSON kubeconfig as well)
KUBECONFIG="$tmp/k/kind-ok" kubectl config view --raw -o json | jq '.users[0].user = {exec: {apiVersion: "client.authentication.k8s.io/v1", command: "plugin", args: ["token"], interactiveMode: "Never"}}' > "$tmp/k/exec"
no "$tmp/k/exec" "" "a user with an exec plugin"

# Two contexts in the file: refused even when the current one is fine.
cp "$tmp/k/kind-ok" "$tmp/k/two"
KUBECONFIG="$tmp/k/two" kubectl config set-cluster c-prod --server https://ABC.eks.amazonaws.com >/dev/null
KUBECONFIG="$tmp/k/two" kubectl config set-context prod --cluster c-prod --user u-kind-adm >/dev/null
no "$tmp/k/two" "" "a file with a second context"

# ~/.kube is never a kind file, however harmless it looks, and a link to a file there is not either.
cp "$tmp/k/kind-ok" "$tmp/home/.kube/config"
no "$tmp/home/.kube/config" "$tmp/home" "a file in ~/.kube"
ln -s "$tmp/home/.kube" "$tmp/k/link"
no "$tmp/k/link/config" "$tmp/home" "a path through a link to ~/.kube"
ln -s "$tmp/home/.kube/config" "$tmp/k/filelink"
no "$tmp/k/filelink" "$tmp/home" "a link to a file in ~/.kube"
ln -s filelink "$tmp/k/filelink2"
no "$tmp/k/filelink2" "$tmp/home" "a link to a link to a file in ~/.kube"
ln -s "$tmp/k/kind-ok" "$tmp/k/okfilelink"
ok "$tmp/k/okfilelink" "$tmp/home" "a link to a good file elsewhere"

# Helm's connection overrides are gone after the guard.
( export HELM_KUBEAPISERVER=https://x HELM_KUBETOKEN=t HELM_KUBECAFILE=/x HELM_KUBEINSECURE_SKIP_TLS_VERIFY=true HELM_KUBEASUSER=u HELM_KUBECONTEXT=prod
  KUBECONFIG="$tmp/k/kind-ok" HOME="$tmp/home" kind_guard
  for v in HELM_KUBEAPISERVER HELM_KUBETOKEN HELM_KUBECAFILE HELM_KUBEINSECURE_SKIP_TLS_VERIFY HELM_KUBEASUSER HELM_KUBECONTEXT; do
    [ -z "${!v:-}" ] || exit 1
  done ) || fail "the guard left a HELM_KUBE* variable set"

# The wrappers name the kubeconfig and the context explicitly.
( KUBECONFIG="$tmp/k/kind-ok" HOME="$tmp/home" kind_guard
  kubectl() { printf '%s\n' "$*"; }; helm() { printf '%s\n' "$*"; }
  kc get ns | grep -q -- "--kubeconfig $tmp/k/kind-ok --context kind-adm get ns" || exit 1
  hc list | grep -q -- "--kubeconfig $tmp/k/kind-ok --kube-context kind-adm list" || exit 1
  kca system:serviceaccount:argocd:x get ns | grep -q -- "--kubeconfig $tmp/k/kind-ok --context kind-adm --as system:serviceaccount:argocd:x get ns" || exit 1
  KCA_GROUP=g kca u get ns | grep -q -- "--as u --as-group g get ns" || exit 1 ) || fail "kc/kca/hc do not pass the kubeconfig and the context"

# ... and refuse an argument that would override them (a value of the tree under test, split by the
# shell, could look like one).
( KUBECONFIG="$tmp/k/kind-ok" HOME="$tmp/home" kind_guard
  kubectl() { printf 'CALLED %s\n' "$*"; }; helm() { printf 'CALLED %s\n' "$*"; }
  for a in --kubeconfig=/x '--kubeconfig' --context=prod --context --cluster=prod --server=https://x --server -s -shttps://x --token=x --token --user=u --user \
    --kube-context=prod --kube-apiserver=https://x --kube-token=x --kube-as-user=u --kube-ca-file=/x --as=u --as --as-group=g --as-uid=1 \
    --certificate-authority=/x --client-certificate=/x --client-key=/x --insecure-skip-tls-verify --tls-server-name=x --username=u --password=p \
    --proxy-url=http://x --post-renderer=/bin/sh; do
    if out=$(kc label secret s "$a" 2>&1) && printf '%s' "$out" | grep -q CALLED; then echo "kc let $a through" >&2; exit 1; fi
    if out=$(hc list "$a" 2>&1) && printf '%s' "$out" | grep -q CALLED; then echo "hc let $a through" >&2; exit 1; fi
    if out=$(kca u get ns "$a" 2>&1) && printf '%s' "$out" | grep -q CALLED; then echo "kca let $a through" >&2; exit 1; fi
  done
  # ordinary arguments, and whatever follows a standalone -- (the command of exec), pass
  kc -n ns get pod x -o name -l a=b -A | grep -q CALLED || exit 1
  kc exec pod/x -- argocd --server localhost | grep -q CALLED || exit 1
  # a hostile label, as admission_full.sh would pass it, as one argument, is refused before kubectl runs
  if kc label secret s '--kubeconfig=/x' 2>/dev/null | grep -q CALLED; then exit 1; fi
  exit 0 ) || fail "kc/kca/hc let an argument through that would choose another cluster"

# kind_guard_verify_with_kind: the file is what kind itself prints.
mkdir -p "$tmp/bin"
cat > "$tmp/bin/kind" <<EOF
#!/usr/bin/env bash
[ "\$1 \$2" = "get kubeconfig" ] || exit 2
[ "\$4" != nosuch ] || { echo "no cluster" >&2; exit 1; }
cat "$tmp/k/\${KIND_FAKE_FILE:-kind-ok}"
EOF
chmod +x "$tmp/bin/kind"
( PATH="$tmp/bin:$PATH" KUBECONFIG="$tmp/k/kind-ok" HOME="$tmp/home" kind_guard && PATH="$tmp/bin:$PATH" kind_guard_verify_with_kind adm ) >/dev/null 2>&1 || fail "verify_with_kind refused kind's own file"
mk "$tmp/k/kind-other-port" kind-adm https://127.0.0.1:41824
( PATH="$tmp/bin:$PATH" KUBECONFIG="$tmp/k/kind-other-port" HOME="$tmp/home" kind_guard && PATH="$tmp/bin:$PATH" kind_guard_verify_with_kind adm ) >/dev/null 2>&1 && fail "verify_with_kind accepted a file with another server"
( PATH="$tmp/bin:$PATH" KUBECONFIG="$tmp/k/kind-ok" HOME="$tmp/home" kind_guard && PATH="$tmp/bin:$PATH" kind_guard_verify_with_kind nosuch ) >/dev/null 2>&1 && fail "verify_with_kind accepted a cluster kind does not know"

echo "✓ kind_guard_test"

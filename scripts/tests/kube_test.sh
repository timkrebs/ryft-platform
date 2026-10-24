#!/usr/bin/env bash
# scripts/lib/kube.sh, the guard that names the cluster (make test-scripts), and its users:
# rescore_all.sh and the Makefile's cluster targets (only `make -n`: nothing is applied).
# A fake kubectl with a few contexts is first on PATH and KUBECONFIG=/dev/null, so nothing
# here reaches a cluster. The lib runs under sh (POSIX: the Makefile's shell).
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir "$t/bin"
fail() { echo "✗ kube_test: $*" >&2; exit 1; }
export KUBECONFIG=/dev/null FAKE_DIR=$t PATH="$t/bin:$PATH"
unset KUBE_CONTEXT

# kubectl: the contexts of $t/config.json; any call with --context only says where it ran.
cat > "$t/bin/kubectl" <<'K'
#!/bin/sh
echo "$*" >> "$FAKE_DIR/calls"
case "$*" in
  "config get-contexts -o name") python3 -c 'import json, sys; [print(c["name"]) for c in json.load(open(sys.argv[1]))["contexts"]]' "$FAKE_DIR/config.json" ;;
  "config view -o json") [ -z "${FAKE_VIEW_FAIL:-}" ] || exit 1; cat "$FAKE_DIR/config.json" ;;
  --context\ *) echo "ran on $2" ;;
  *) echo "fake kubectl: unexpected (no --context): $*" >&2; exit 3 ;;
esac
K
# shellcheck disable=SC2016 # the fakes expand $* and $FAKE_DIR when they run
for tool in helm aws podman; do printf '#!/bin/sh\necho "%s $*" >> "$FAKE_DIR/calls"\n' "$tool" > "$t/bin/$tool"; done
chmod +x "$t/bin/"*
# Contexts as `kubectl config view -o json` shows them: two local ones, and EKS by each sign
# (name, cluster ARN, server, token), alone.
python3 - "$t/config.json" <<'PY'
import json, sys
def ctx(name, cluster, server, user=None):
    return name, cluster, server, user or {"client-certificate-data": "DATA+OMITTED"}
token = lambda *a: {"exec": {"command": a[0], "args": list(a[1:]), "apiVersion": "client.authentication.k8s.io/v1beta1"}}
arn = "arn:aws:eks:eu-central-1:000000000000:cluster/"
rows = [ctx("kind-ryft", "kind-ryft", "https://127.0.0.1:6443"),
        ctx("docker-desktop", "docker-desktop", "https://kubernetes.docker.internal:6443"),
        ctx("ryft-prod-euc1", arn + "ryft-prod-euc1", "https://ABC123.gr7.eu-central-1.eks.amazonaws.com",
            token("aws", "--region", "eu-central-1", "eks", "get-token", "--cluster-name", "ryft-prod-euc1")),
        ctx("ryft-prod-ops", "ops", "https://10.0.0.1"),
        ctx(arn + "renamed", "renamed", "https://10.0.0.2"),
        ctx("by-cluster", arn + "other", "https://10.0.0.3"),
        ctx("by-server", "plain", "https://def456.yl4.eu-west-1.eks.amazonaws.com"),
        ctx("by-token", "proxied", "https://10.0.0.4", token("/usr/local/bin/aws", "eks", "get-token", "--cluster-name", "x")),
        ctx("by-authenticator", "iam", "https://10.0.0.5", token("aws-iam-authenticator", "token", "-i", "x"))]
json.dump({"contexts": [{"name": n, "context": {"cluster": c, "user": n + "-user"}} for n, c, s, u in rows],
           "clusters": [{"name": c, "cluster": {"server": s}} for n, c, s, u in rows],
           "users": [{"name": n + "-user", "user": u} for n, c, s, u in rows]}, open(sys.argv[1], "w"))
PY

lib() { # <shell code using the lib> → its exit code; calls and stderr in $t
  rm -f "$t/calls"; touch "$t/calls"
  local rc=0
  sh -c '. "$1"; shift; eval "$1"' sh "$ROOT/scripts/lib/kube.sh" "$1" > "$t/out" 2> "$t/err" || rc=$?
  return "$rc"
}
calls() { cat "$t/calls"; }
expect() { # <want rc> <code>
  local rc=0; lib "$2" || rc=$?
  [ "$rc" = "$1" ] || fail "'$2' exited $rc, want $1: $(cat "$t/err")"
}

# No name: nothing runs. A name that is not one: nothing runs. An unknown one: only the listing.
expect 2 'kube_context'; [ -z "$(calls)" ] || fail "no context still called: $(calls)"
grep -q 'KUBE_CONTEXT=<context>' "$t/err" || fail "the refusal does not say what to set: $(cat "$t/err")"
# shellcheck disable=SC2016 # a literal $(id): it must be refused, not run
for bad in 'kind-ryft;id' '-x' 'a b' 'kind-ryft$(id)'; do
  expect 2 "kube_context '$bad'"; [ -z "$(calls)" ] || fail "'$bad' still called: $(calls)"
done
expect 2 'kube_context nope'; [ "$(calls)" = "config get-contexts -o name" ] || fail "an unknown context: $(calls)"
expect 2 'kube_context kind'; [ "$(calls)" = "config get-contexts -o name" ] || fail "a prefix is not a context: $(calls)"

# A listed one: announced, and every call carries it. From KUBE_CONTEXT as well.
expect 0 'kube_context kind-ryft && k get pods && k -n x logs y'
[ "$(cat "$t/err")" = "→ kind-ryft" ] || fail "not announced: $(cat "$t/err")"
[ "$(calls)" = "config get-contexts -o name
--context kind-ryft get pods
--context kind-ryft -n x logs y" ] || fail "calls: $(calls)"
expect 0 'KUBE_CONTEXT=docker-desktop; kube_context && k version'
calls | grep -qx -- '--context docker-desktop version' || fail "KUBE_CONTEXT: $(calls)"
# k refuses before kube_context, and after KUBE_CONTEXT changed behind its back.
expect 2 'k get pods'; [ -z "$(calls)" ] || fail "k without kube_context called: $(calls)"
expect 2 'KUBE_CONTEXT=kind-ryft; k get pods'; [ -z "$(calls)" ] || fail "k with an unchecked KUBE_CONTEXT called: $(calls)"
expect 2 'kube_context kind-ryft; KUBE_CONTEXT=ryft-prod-euc1; k get pods'
if calls | grep -q -- '--context ryft-prod-euc1'; then fail "k used a context that was not checked"; fi

# --local: the local ones pass; every EKS sign alone refuses, and an unreadable config refuses.
for c in kind-ryft docker-desktop; do expect 0 "kube_context --local $c"; done
for c in ryft-prod-euc1 ryft-prod-ops arn:aws:eks:eu-central-1:000000000000:cluster/renamed by-cluster by-server by-token by-authenticator; do
  expect 2 "kube_context --local '$c'"
  grep -q "reaches EKS" "$t/err" || fail "$c: $(cat "$t/err")"
  if calls | grep -q -- '--context'; then fail "$c: a call ran on it"; fi
done
grep -q 'an EKS token' "$t/err" || fail "the token sign is not named: $(cat "$t/err")"
FAKE_VIEW_FAIL=1 expect 2 'kube_context --local kind-ryft'
grep -q 'cannot be read' "$t/err" || fail "an unreadable config: $(cat "$t/err")"
expect 0 'kube_context ryft-prod-euc1'  # without --local it may be EKS

# rescore_all.sh: the days are checked and the cluster is named before anything runs.
rescore() { rm -f "$t/calls"; touch "$t/calls"; local rc=0; sh "$ROOT/scripts/rescore_all.sh" "$@" > "$t/out" 2> "$t/err" || rc=$?; return "$rc"; }
for args in "" "30" "abc" "0" "401" "30 40" "--days 3"; do
  rc=0; read -ra words <<< "$args"; rescore ${words[@]+"${words[@]}"} || rc=$?
  [ "$rc" = 2 ] && [ -z "$(calls)" ] || fail "rescore_all.sh $args without a context: $rc, $(calls) $(cat "$t/err")"
done
for args in "abc --context kind-ryft" "401 --context kind-ryft"; do
  rc=0; read -ra words <<< "$args"; rescore "${words[@]}" || rc=$?
  [ "$rc" = 2 ] && [ -z "$(calls)" ] || fail "rescore_all.sh $args: $rc, $(calls)"
done
rescore 30 --context kind-ryft || fail "rescore_all.sh 30 --context kind-ryft: $(cat "$t/err")"
[ "$(calls)" = "config get-contexts -o name
--context kind-ryft -n ryft-scoring exec deploy/scoring -- /app rescore -days 30" ] || fail "rescore calls: $(calls)"
grep -q 'on kind-ryft' "$t/err" || fail "rescore does not name the cluster: $(cat "$t/err")"
KUBE_CONTEXT=kind-ryft rescore || fail "rescore_all.sh with KUBE_CONTEXT: $(cat "$t/err")"
calls | grep -qx -- '--context kind-ryft -n ryft-scoring exec deploy/scoring -- /app rescore -days 400' || fail "default days: $(calls)"
RESCORE_LOCAL=1 CONTAINER=podman rescore 7 || fail "RESCORE_LOCAL: $(cat "$t/err")"
[ "$(calls)" = "podman compose exec -T scoring /app rescore -days 7" ] || fail "RESCORE_LOCAL calls: $(calls)"

# The Makefile, dry (`make -n` prints and does not run; its sub-make is dry too): without a
# context every cluster target stops; with one, every kubectl call that reaches a cluster and
# every helm release carries it, after the guard. The local targets use the local guard.
targets="k8s-apply-eks k8s-rabbitmq monitoring-apply k8s-check-auth k8s-apply-local k8s-delete-local"
rm -f "$t/calls"
for target in $targets; do
  rc=0; make -s -n -C "$ROOT" "$target" > "$t/out" 2>&1 || rc=$?
  if [ "$rc" = 0 ] || ! grep -q 'KUBE_CONTEXT is not set' "$t/out"; then fail "make -n $target without a context: $rc $(cat "$t/out")"; fi
  rc=0; make -s -n -C "$ROOT" "$target" KUBE_CONTEXT=kind-ryft RABBITMQ=deploy/rabbitmq > "$t/out" 2>&1 || rc=$?
  [ "$rc" = 0 ] || fail "make -n $target: $rc $(cat "$t/out")"
  python3 - "$target" "$t/out" <<'PY' || fail "make -n $target (above): $(cat "$t/out")"
import re, sys
target, lines = sys.argv[1], open(sys.argv[2]).read().splitlines()
local = target in ("k8s-apply-local", "k8s-delete-local")
guard = [i for i, l in enumerate(lines) if ("kube_context --local 'kind-ryft'" if local else "kube_context 'kind-ryft'") in l]
assert guard, "no guard"
cluster = []  # (line, call) of every kubectl or helm call that reaches a cluster
for i, l in enumerate(lines):
    for m in re.finditer(r"(?:^|[^-\w'])(kubectl|helm)\s+(\S+)(\s+\S+)?", l):
        tool, first, second = m.group(1), m.group(2), (m.group(3) or "").strip()
        if (tool, first) in (("kubectl", "kustomize"), ("helm", "repo"), ("helm", "template")):
            continue  # offline
        cluster.append((i, m.group(0)))
        want = "--context" if tool == "kubectl" else "--kube-context"
        assert first == want and second == "'kind-ryft'", (l, m.group(0))
assert cluster, "no cluster call"
assert min(guard) < min(i for i, _ in cluster), "the guard is not first"
PY
done
for target in k8s-apply-local k8s-delete-local; do
  grep -q "kube_context --local" <(make -s -n -C "$ROOT" "$target" KUBE_CONTEXT=kind-ryft 2>&1) || fail "$target has no local guard"
done
[ ! -s "$t/calls" ] || fail "make -n ran something: $(calls)"

echo "✓ kube_test: no context no call, a listed context on every call, EKS refused by name, cluster, server or token for the local targets, rescore_all.sh, the Makefile's cluster targets (dry)"

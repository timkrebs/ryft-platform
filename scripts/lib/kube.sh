# shellcheck shell=sh
# The cluster an operator script or a Makefile target talks to is named, never kubectl's
# current context: on 2026-10-23 a usage check of scripts/dlq.sh read the production
# broker's Secrets because the current context happened to be ryft-prod-euc1. Sourced by
# dlq.sh, rescore_all.sh, trace.sh and the Makefile's cluster targets; POSIX sh (the
# Makefile's /bin/sh, macOS's bash 3.2). It reads only the local kubeconfig.
#
#   KUBE_CONTEXT=<name>            the context, exactly as `kubectl config get-contexts -o name` lists it
#
#   kube_context [--local] [name]  name (default $KUBE_CONTEXT) must be set, a plain context name and
#                                  listed; then KUBE_CONTEXT is it and "→ <name>" goes to stderr
#                                  (not with KUBE_QUIET set). --local also refuses a context that
#                                  reaches EKS (kube_refuse_eks). Returns 2 otherwise, before any
#                                  kubectl call when no name is set.
#   k <kubectl args>               kubectl --context "$KUBE_CONTEXT" <args>, only after kube_context
#   kube_refuse_eks <name>         returns 1 (and says why) when the context reaches EKS, or when its
#                                  entry in the kubeconfig cannot be read or has no cluster entry
#                                  or server (fails closed)

kube_context() {
  _kube_local=
  if [ "${1:-}" = --local ]; then
    _kube_local=1
    shift
  fi
  _kube_name=${1:-${KUBE_CONTEXT:-}}
  if [ -z "$_kube_name" ]; then
    echo "✗ no cluster named: set KUBE_CONTEXT=<context> (one of \`kubectl config get-contexts -o name\`); kubectl's current context is never used" >&2
    return 2
  fi
  case $_kube_name in
    -* | *[!A-Za-z0-9._:/@+-]*)
      echo "✗ KUBE_CONTEXT \"$_kube_name\" is not a context name" >&2
      return 2 ;;
  esac
  # grep reads to the end (not -q): under pipefail an early exit could fail kubectl with SIGPIPE.
  if ! kubectl config get-contexts -o name 2>/dev/null | grep -Fx -- "$_kube_name" >/dev/null; then
    echo "✗ KUBE_CONTEXT $_kube_name: no such context in the kubeconfig (kubectl config get-contexts -o name)" >&2
    return 2
  fi
  if [ -n "$_kube_local" ] && ! kube_refuse_eks "$_kube_name"; then
    return 2
  fi
  KUBE_CONTEXT=$_kube_name
  _kube_checked=$_kube_name
  [ -n "${KUBE_QUIET:-}" ] || echo "→ $_kube_name" >&2
}

k() {
  if [ -z "${KUBE_CONTEXT:-}" ] || [ "$KUBE_CONTEXT" != "${_kube_checked:-}" ]; then
    echo "✗ k: no checked KUBE_CONTEXT (kube_context first)" >&2
    return 2
  fi
  kubectl --context "$KUBE_CONTEXT" "$@"
}

# EKS, by any of four signs: the name (ryft-prod-*, an EKS ARN), the cluster entry's name (an
# EKS ARN, as `aws eks update-kubeconfig` writes it), the API server (*.eks.amazonaws.com) or
# the sign-in (an `aws … eks get-token` or aws-iam-authenticator exec). One is enough.
kube_refuse_eks() {
  _kube_why=$(kubectl config view -o json 2>/dev/null | KUBE_CHECK=$1 python3 -c '
import json, os, sys
from urllib.parse import urlsplit
sys.excepthook = lambda *a: sys.exit(3)  # anything unreadable: not used
name = os.environ["KUBE_CHECK"]
cfg = json.load(sys.stdin)
def entry(kind, n):
    for x in cfg.get(kind) or []:
        if x.get("name") == n:
            return x.get(kind[:-1]) or {}
    return None
ctx = entry("contexts", name)
if ctx is None:
    sys.exit(2)
cluster = ctx.get("cluster") or ""
cl = entry("clusters", cluster)
host = (urlsplit((cl or {}).get("server") or "").hostname or "").lower()
ex = (entry("users", ctx.get("user") or "") or {}).get("exec") or {}
argv = [os.path.basename(ex.get("command") or "")] + [str(a) for a in ex.get("args") or []]
why = []
if name.startswith(("ryft-prod-", "arn:aws:eks:")):
    why.append("its name")
if cluster.startswith(("ryft-prod-", "arn:aws:eks:")):
    why.append("its cluster " + cluster)
if host.endswith((".eks.amazonaws.com", ".eks.amazonaws.com.cn")):
    why.append("its server " + host)
if argv[0] == "aws-iam-authenticator" or ("eks" in argv and "get-token" in argv):
    why.append("an EKS token")
print(", ".join(why))
if why:
    sys.exit(1)
if cl is None or not host:
    sys.exit(3)  # no cluster entry or no server: nothing to judge it by
')
  case $? in
    0) return 0 ;;
    1) echo "✗ $1 reaches EKS ($_kube_why): this is for a local or self-hosted cluster, never EKS" >&2 ;;
    *) echo "✗ $1: its cluster cannot be read from the kubeconfig, so it is not used" >&2 ;;
  esac
  return 1
}

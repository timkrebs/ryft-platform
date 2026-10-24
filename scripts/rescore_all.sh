#!/bin/sh
# Rescores every user's history with the current scoring rules, after a rule or
# constant changed (docs/SCORING.md). Runs `scoring rescore` inside the running
# scoring pod: scoring's own least-privilege role and credentials, no admin key,
# no user tokens, no recompute gate. Days are rescored oldest first per user, so the
# baselines build up as they would have. The last log line names the model
# version every row now carries (internal/scoring/version.go, GET /v1/model).
#
#   KUBE_CONTEXT=<context> scripts/rescore_all.sh [days]   # a cluster, named; days 1 to 400, default 400
#   scripts/rescore_all.sh [days] --context <context>
#   RESCORE_LOCAL=1 scripts/rescore_all.sh 30              # the compose stack
#
# It writes every user's scores, so the cluster is never kubectl's current context
# (scripts/lib/kube.sh): `kubectl config get-contexts -o name` lists the names.
set -eu
. "$(dirname "$0")/lib/kube.sh"
usage() { sed -n '9,11s/^#   //p' "$0" >&2; exit 2; }
DAYS='' ctx=''
while [ $# -gt 0 ]; do
  case $1 in
    --context) [ $# -ge 2 ] && [ -n "$2" ] || usage; ctx=$2; shift ;;
    --context=?*) ctx=${1#--context=} ;;
    -*) usage ;;
    *) [ -z "$DAYS" ] || usage; DAYS=$1 ;;
  esac
  shift
done
DAYS=${DAYS:-400}
case $DAYS in '' | 0* | *[!0-9]* | ?????*) usage ;; esac
[ "$DAYS" -le 400 ] || usage
VERSION=$(sed -n 's/^const ModelVersion = "\(.*\)"$/\1/p' "$(dirname "$0")/../backend/internal/scoring/version.go")
if [ "${RESCORE_LOCAL:-}" = 1 ]; then
  echo "rescoring the last $DAYS days of every user to model $VERSION on the compose stack" >&2
  exec ${CONTAINER:-podman} compose exec -T scoring /app rescore -days "$DAYS"
fi
kube_context "$ctx" || exit 2
echo "rescoring the last $DAYS days of every user to model $VERSION on $KUBE_CONTEXT" >&2
k -n ryft-scoring exec deploy/scoring -- /app rescore -days "$DAYS"

#!/bin/sh
# Rescores every user's history with the current scoring rules, after a rule or
# constant changed (docs/SCORING.md). Runs `scoring rescore` inside the running
# scoring pod: scoring's own least-privilege role and credentials, no admin key,
# no user tokens, no recompute gate. Days are rescored oldest first per user, so the
# baselines build up as they would have.
#
#   scripts/rescore_all.sh [days]        # EKS, default 400 (the maximum)
#   RESCORE_LOCAL=1 scripts/rescore_all.sh 30   # the compose stack
set -eu
DAYS=${1:-400}
if [ "${RESCORE_LOCAL:-}" = 1 ]; then
  exec ${CONTAINER:-podman} compose exec -T scoring /app rescore -days "$DAYS"
fi
exec kubectl -n ryft-scoring exec deploy/scoring -- /app rescore -days "$DAYS"

#!/usr/bin/env bash
# One request across the services (docs/OPERATIONS.md → Tracing a request): every
# log line of api, ingest, scoring and notify that carries the request id, from all
# pods, as compact JSON, then the account it belonged to (GET /v1/admin/users/{id}).
#
#   scripts/trace.sh <request-id> [--since 48h] [--no-user]
#
# A request id with a lost hyphen (01a15a69-71427a8b-aba2-…, as the app once showed
# it) is put back into shape. Pod logs reach back to the pod's start; older lines are
# in CloudWatch (docs/OBSERVABILITY.md → Logs). Needs kubectl and python3.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib/admin.sh"
since=48h
user=1
id=""
while [ $# -gt 0 ]; do
  case "$1" in
    --since) since=$2; shift 2 ;;
    --no-user) user=""; shift ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) id=$1; shift ;;
  esac
done
[ -n "$id" ] || { echo "usage: scripts/trace.sh <request-id> [--since 48h] [--no-user]" >&2; exit 2; }

# A UUID's 32 hex digits, however the hyphens came out, back into 8-4-4-4-12.
hex=$(printf '%s' "$id" | tr -d ' -' | tr 'A-F' 'a-f')
if printf '%s' "$hex" | grep -Eq '^[0-9a-f]{32}$'; then
  norm="${hex:0:8}-${hex:8:4}-${hex:12:4}-${hex:16:4}-${hex:20:12}"
  [ "$norm" = "$id" ] || echo "request id: $id → $norm" >&2
  id=$norm
fi

lines=$(mktemp)
for svc in api ingest scoring notify; do
  # --tail=-1: with -l, kubectl shows only the last 10 lines of each pod otherwise.
  kubectl -n "ryft-$svc" logs -l "app=$svc" --tail=-1 --prefix --since="$since" --max-log-requests=20 2>/dev/null \
    | grep -F "$id" >> "$lines" || true
done

userfile=$(mktemp)
trap 'rm -f "$lines" "$userfile"' EXIT
python3 - "$lines" "$userfile" <<'PY'
import json, sys
users = []
for raw in open(sys.argv[1]):
    pod, _, js = raw.partition("] ")
    pod = pod.lstrip("[").replace("pod/", "").split("/")[0]
    try:
        d = json.loads(js)
    except ValueError:
        print(json.dumps({"pod": pod, "line": js.strip()[:300]}))
        continue
    out = {"time": d.get("time"), "service": d.get("service"), "pod": pod}
    for k in ("method", "path", "status", "code", "gate", "dur_ms", "route", "day", "model_version", "inputs_changed"):
        if k in d:
            out[k] = d[k]
    if d.get("user_id"):
        out["user"] = d["user_id"]
        if d["user_id"] not in users:
            users.append(d["user_id"])
    if d.get("msg") != "http":
        out["msg"] = d.get("msg")
    if d.get("err"):
        out["error"] = d["err"]
    print(json.dumps(out, ensure_ascii=False))
open(sys.argv[2], "w").write(" ".join(users))
PY
users=$(cat "$userfile")

if [ ! -s "$lines" ]; then
  echo "✗ $id: in no pod's log of the last $since (pods restarted since? then CloudWatch, docs/OBSERVABILITY.md → Logs)" >&2
  exit 1
fi
[ -n "$user" ] && [ -n "$users" ] || exit 0
ryft_admin_key
for u in $users; do
  ryft_admin GET "/v1/admin/users/$u" | python3 "$HERE/lib/user_summary.py" || true
done

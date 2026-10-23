#!/usr/bin/env bash
# RabbitMQ's dead letters (dlq.scoring, dlq.notify; D99, docs/OPERATIONS.md → RabbitMQ)
# through the management API:
#
#   scripts/dlq.sh list                    count per queue, then one line per letter (left in place)
#   scripts/dlq.sh show <queue> [n]        the first n letters in full (default 10; left in place)
#   scripts/dlq.sh replay <queue> [n|all]  take letters off and publish each to ryft.events with its
#                                          original key, so its consumer handles it again, as a
#                                          redelivery (x-ryft-replayed) with all its attempts
#   scripts/dlq.sh purge <queue>           delete every letter (asks first)
#
# <queue> is dlq.scoring or dlq.notify (or scoring / notify). notify's letters are not
# replayed without --force: a push hours late is wrong (list and purge them).
#
# By default it port-forwards to the cluster's management API (svc/rabbitmq, 15671),
# verifies the broker's certificate against the internal CA (Secret rabbitmq-tls) and
# uses the operator's default user (Secret rabbitmq-default-user). Elsewhere (compose):
#   RABBITMQ_MGMT_URL=http://localhost:15672 RABBITMQ_USER=admin RABBITMQ_PASSWORD=… scripts/dlq.sh list
set -euo pipefail
cmd=${1:-list}
force=
for a in "$@"; do [ "$a" = --force ] && force=1; done
queue_of() {
  case "${1:-}" in
    dlq.scoring|scoring) echo dlq.scoring ;;
    dlq.notify|notify) echo dlq.notify ;;
    *) echo "✗ dlq.sh: the queue is dlq.scoring or dlq.notify" >&2; exit 2 ;;
  esac
}
# What needs no broker first: a usage error or a refused replay never reaches a cluster.
q=
case "$cmd" in
  list) ;;
  show|replay|purge) q=$(queue_of "${2:-}") ;;
  *) awk 'NR > 1 && !/^#/ { exit } NR > 1' "$0"; exit 2 ;;  # the comment above
esac
if [ "$cmd" = replay ] && [ "$q" = dlq.notify ] && [ -z "$force" ]; then
  echo "✗ dlq.sh: notify's dead letters are not replayed (a push hours late is wrong); purge them, or --force" >&2; exit 2
fi
VHOST=${RABBITMQ_VHOST:-ryft}
tmp=$(mktemp -d)
pf=
cleanup() { [ -z "$pf" ] || kill "$pf" 2>/dev/null || true; rm -rf "$tmp"; }
trap cleanup EXIT

if [ -z "${RABBITMQ_MGMT_URL:-}" ]; then
  ns=ryft-rabbitmq
  kubectl -n $ns get secret rabbitmq-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > "$tmp/ca.crt"
  RABBITMQ_USER=$(kubectl -n $ns get secret rabbitmq-default-user -o jsonpath='{.data.username}' | base64 -d)
  RABBITMQ_PASSWORD=$(kubectl -n $ns get secret rabbitmq-default-user -o jsonpath='{.data.password}' | base64 -d)
  port=$((20000 + RANDOM % 20000))
  kubectl -n $ns port-forward svc/rabbitmq "$port:15671" >/dev/null 2>&1 &
  pf=$!
  disown "$pf"
  for _ in $(seq 1 30); do (echo > /dev/tcp/127.0.0.1/$port) 2>/dev/null && break; sleep 0.3; done
  # The certificate names the Service, so connect to that name through the forward.
  RABBITMQ_MGMT_URL=https://rabbitmq.$ns.svc:$port
  CURL_EXTRA=(--cacert "$tmp/ca.crt" --resolve "rabbitmq.$ns.svc:$port:127.0.0.1")
else
  : "${RABBITMQ_USER:?with RABBITMQ_MGMT_URL set RABBITMQ_USER and RABBITMQ_PASSWORD}" "${RABBITMQ_PASSWORD:?}"
  CURL_EXTRA=()
fi
export RABBITMQ_MGMT_URL RABBITMQ_USER RABBITMQ_PASSWORD VHOST
api() { # METHOD PATH [JSON]
  curl -fsS ${CURL_EXTRA[@]+"${CURL_EXTRA[@]}"} -u "$RABBITMQ_USER:$RABBITMQ_PASSWORD" -H 'content-type: application/json' \
    -X "$1" ${3:+--data "$3"} "$RABBITMQ_MGMT_URL$2"
}
get() { # QUEUE COUNT ACKMODE → JSON array
  api POST "/api/queues/$VHOST/$1/get" "{\"count\": $2, \"ackmode\": \"$3\", \"encoding\": \"auto\", \"truncate\": 50000}"
}
# The letters themselves, not the queue's message count: the management API updates
# that only every few seconds. At most 1000 at a time.
MAX=1000

case "$cmd" in
  list)
    for q in dlq.scoring dlq.notify; do
      get "$q" $MAX ack_requeue_true > "$tmp/$q.json"
      echo "$q: $(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$tmp/$q.json")"
      python3 -c '
import json, sys
for m in json.load(sys.stdin):
    p = m.get("properties", {}); h = p.get("headers", {})
    print(json.dumps({"subject": h.get("Ryft-Subject"), "attempts": h.get("Ryft-Deliveries"),
        "error": h.get("Ryft-Error"), "message_id": p.get("message_id"), "dead_lettered_by_broker": "x-death" in h,
        "replayed_before": h.get("x-ryft-replayed") is True}))' < "$tmp/$q.json"
    done ;;
  show)
    get "$q" "${3:-10}" ack_requeue_true | python3 -m json.tool ;;
  replay)
    n=${3:-all}
    case "$n" in all|--force) n=$MAX ;; esac
    get "$q" "$n" ack_requeue_false > "$tmp/taken.json"
    [ "$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$tmp/taken.json")" -gt 0 ] || { echo "$q is empty"; exit 0; }
    # Per letter: the publish to ryft.events with its original key, and the one back
    # into its dead-letter queue if that fails (nothing is lost).
    python3 - "$tmp/taken.json" "${q#dlq.}" "$tmp/replay.jsonl" "$tmp/back.jsonl" <<'PY'
import json, sys
taken, consumer = json.load(open(sys.argv[1])), sys.argv[2]
death = ("x-death", "x-ryft-attempt", "x-first-death-exchange", "x-first-death-queue", "x-first-death-reason",
         "x-last-death-exchange", "x-last-death-queue", "x-last-death-reason")
# amqpbus.HeaderReplayed: the consumer takes the letter as a redelivery (events.Redelivered), so
# scoring finishes a re-score its failed attempts left half-done (D112). Only the boolean true
# counts. Without x-ryft-attempt the letter has all its attempts again.
REPLAYED = "x-ryft-replayed"
with open(sys.argv[3], "w") as replay, open(sys.argv[4], "w") as back:
    for m in taken:
        p = m.get("properties", {})
        h = dict(p.get("headers", {}) or {})
        key = h.get("Ryft-Subject") or p.get("type") or ""
        def body(k, headers):
            return {"properties": {"content_type": p.get("content_type", "application/json"), "delivery_mode": 2,
                    "message_id": p.get("message_id", ""), "type": p.get("type", key), "headers": headers},
                    "routing_key": k, "payload": m["payload"], "payload_encoding": m.get("payload_encoding", "string")}
        clean = {k: v for k, v in h.items() if not k.startswith("Ryft-") and k not in death}
        clean[REPLAYED] = True
        replay.write(json.dumps({"key": key, "body": body(key, clean)}) + "\n")
        back.write(json.dumps(body(consumer, h)) + "\n")
PY
    ok=0; kept=0
    exec 3<"$tmp/replay.jsonl" 4<"$tmp/back.jsonl"
    while IFS= read -r line <&3 && IFS= read -r back <&4; do
      key=$(printf '%s' "$line" | python3 -c 'import json,sys; print(json.load(sys.stdin)["key"])')
      body=$(printf '%s' "$line" | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["body"]))')
      if [ -n "$key" ] && api POST "/api/exchanges/$VHOST/ryft.events/publish" "$body" 2>/dev/null | grep -q '"routed":true'; then
        ok=$((ok + 1))
      else
        api POST "/api/exchanges/$VHOST/ryft.dlx/publish" "$back" >/dev/null
        kept=$((kept + 1))
      fi
    done
    echo "replayed $ok, put back $kept"
    [ "$kept" = 0 ]
    ;;
  purge)
    n=$(get "$q" $MAX ack_requeue_true | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
    read -r -p "delete all $n letters in $q? type PURGE: " answer
    [ "$answer" = PURGE ] || { echo "nothing deleted"; exit 1; }
    api DELETE "/api/queues/$VHOST/$q/contents" && echo "$q purged" ;;
esac

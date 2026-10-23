#!/usr/bin/env bash
# dlq.sh against a fake management API (make test-scripts): a replay publishes each letter to
# ryft.events with its key, marked as a replay (x-ryft-replayed: true, which the consumer takes
# as a redelivery), without the Ryft-* and death headers and without x-ryft-attempt (all its
# attempts again); a letter that is not routed goes back to its dead-letter queue as it was;
# notify's letters stay unreplayed without --force; a usage error or a refusal reaches no
# cluster. The real broker's side is amqpbus's TestReplayedLetterIsARedelivery.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir "$t/bin"
fail() { echo "✗ dlq_test: $*" >&2; exit 1; }

# kubectl must never run here: it would reach whatever cluster the kubeconfig names.
cat > "$t/bin/kubectl" <<K
#!/bin/sh
echo "\$*" >> "$t/kubectl.called"
echo "fake kubectl: dlq_test must not reach a cluster" >&2
exit 3
K
# curl: the management API. Records each call (method, path, JSON body) and answers /get from
# <queue>.json, a publish to ryft.events with FAKE_ROUTED (default true), any other publish routed
# (compact, as RabbitMQ writes it: dlq.sh greps for "routed":true).
cat > "$t/bin/curl" <<'C'
#!/usr/bin/env python3
import json, os, sys
from urllib.parse import urlsplit
args, method, data = sys.argv[1:], "GET", None
for i, a in enumerate(args):
    if a == "-X": method = args[i + 1]
    if a == "--data": data = json.loads(args[i + 1])
path = urlsplit(args[-1]).path
with open(os.environ["FAKE_CALLS"], "a") as f:
    f.write(json.dumps({"method": method, "path": path, "data": data}) + "\n")
if path.endswith("/get"):
    sys.stdout.write(open(os.path.join(os.environ["FAKE_DIR"], path.split("/")[-2] + ".json")).read())
elif path.endswith("/ryft.events/publish"):
    print('{"routed":%s}' % ("true" if os.environ.get("FAKE_ROUTED", "true") == "true" else "false"))
elif path.endswith("/publish"):
    print('{"routed":true}')
C
chmod +x "$t/bin/kubectl" "$t/bin/curl"

# Letters as the management API's /get returns them: one after six failed attempts (with the
# tiers' death headers and a header of its own), one replayed before and dead-lettered again,
# one whose marker is malformed (a string).
death='"x-ryft-attempt": 5, "x-death": [{"count": 1, "exchange": "ryft.retry", "queue": "scoring.retry.5m", "reason": "expired",
  "routing-keys": ["scoring.retry.5m"], "time": 1761200000}], "x-first-death-exchange": "ryft.retry",
  "x-first-death-queue": "scoring.retry.5s", "x-first-death-reason": "expired", "x-last-death-exchange": "ryft.retry",
  "x-last-death-queue": "scoring.retry.5m", "x-last-death-reason": "expired"'
ryft='"Ryft-Subject": "ingested", "Ryft-Consumer": "scoring", "Ryft-Deliveries": "6", "Ryft-Error": "day 2026-10-20: boom"'
letter() { # <message id> <extra headers>
  printf '{"payload_bytes": 1, "redelivered": false, "exchange": "ryft.dlx", "routing_key": "scoring", "message_count": 0,
  "properties": {"message_id": "%s", "type": "ingested", "delivery_mode": 2, "content_type": "application/json",
  "headers": {%s, %s%s}}, "payload": "{\\"user_id\\":\\"00000000-0000-0000-0000-000000000001\\",\\"days\\":[\\"2026-10-20\\"],\\"reason\\":\\"sync\\"}",
  "payload_encoding": "string"}' "$1" "$ryft" "$death" "$2"
}
echo "[$(letter first ', "trace": "kept"'), $(letter again ', "x-ryft-replayed": true'), $(letter odd ', "x-ryft-replayed": "yes"')]" > "$t/dlq.scoring.json"
ryft='"Ryft-Subject": "scored", "Ryft-Consumer": "notify", "Ryft-Deliveries": "6", "Ryft-Error": "apns: 503"'
echo "[$(letter push '')]" | sed 's/"type": "ingested"/"type": "scored"/' > "$t/dlq.notify.json"
python3 -c 'import json, sys; [json.load(open(f)) for f in sys.argv[1:]]' "$t/dlq.scoring.json" "$t/dlq.notify.json" || fail "bad fixtures"

export FAKE_DIR=$t FAKE_CALLS=$t/calls.jsonl
export RABBITMQ_USER=dlq-test RABBITMQ_PASSWORD=$RANDOM$RANDOM
run() { # mgmt|cluster <args>: dlq.sh with the fakes first on PATH, pointed at the fake API (mgmt) or
  # in its cluster mode (no RABBITMQ_MGMT_URL: it would port-forward with kubectl)
  : > "$FAKE_CALLS"
  local mode=$1; shift
  if [ "$mode" = mgmt ]; then
    PATH="$t/bin:$PATH" RABBITMQ_MGMT_URL=http://dlq-test.invalid bash "$ROOT/scripts/dlq.sh" "$@"
  else
    (unset RABBITMQ_MGMT_URL; PATH="$t/bin:$PATH" bash "$ROOT/scripts/dlq.sh" "$@")
  fi
}
check() { # <python over calls (list of dicts), letters (the fixture)>
  python3 -c '
import json, sys
calls = [json.loads(l) for l in open(sys.argv[1])]
letters = json.load(open(sys.argv[2]))
exec(sys.argv[3])' "$FAKE_CALLS" "$1" "$2"
}

# Replay: each letter to ryft.events with its key, the marker set to true (also over a
# malformed one), none of the dead letter's headers, the rest of the message as it was.
rc=0; out=$(run mgmt replay dlq.scoring all 2>"$t/err") || rc=$?
[ "$rc" = 0 ] || fail "replay exited $rc: $(cat "$t/err")"
[ "$out" = "replayed 3, put back 0" ] || fail "replay said: $out"
check "$t/dlq.scoring.json" '
gets = [c for c in calls if c["path"].endswith("/get")]
assert [g["path"] for g in gets] == ["/api/queues/ryft/dlq.scoring/get"] and gets[0]["data"]["ackmode"] == "ack_requeue_false", gets
pubs = [c for c in calls if c["path"].endswith("/publish")]
assert [p["path"] for p in pubs] == ["/api/exchanges/ryft/ryft.events/publish"] * 3, pubs
death = ("x-death", "x-ryft-attempt", "x-first-death-exchange", "x-first-death-queue", "x-first-death-reason",
         "x-last-death-exchange", "x-last-death-queue", "x-last-death-reason")
for p, m, own in zip(pubs, letters, [{"trace": "kept"}, {}, {}]):
    b, h = p["data"], p["data"]["properties"]["headers"]
    assert h.get("x-ryft-replayed") is True, h
    assert not [k for k in h if k.startswith("Ryft-") or k in death], h
    assert h == dict(own, **{"x-ryft-replayed": True}), h
    assert b["routing_key"] == "ingested" and b["payload"] == m["payload"] and b["payload_encoding"] == "string", b
    pr = b["properties"]
    assert (pr["message_id"], pr["type"], pr["delivery_mode"], pr["content_type"]) == (m["properties"]["message_id"], "ingested", 2, "application/json"), pr
' || fail "the replayed messages (above)"
[ ! -e "$t/kubectl.called" ] || fail "kubectl ran: $(cat "$t/kubectl.called")"

# Not routed: back into the dead-letter queue exactly as it was (no marker added), and a failure.
export FAKE_ROUTED=false
rc=0; out=$(run mgmt replay scoring 2>"$t/err") || rc=$?
unset FAKE_ROUTED
[ "$rc" = 1 ] || fail "an unrouted replay must fail (got $rc)"
[ "$out" = "replayed 0, put back 3" ] || fail "unrouted replay said: $out"
check "$t/dlq.scoring.json" '
back = [c["data"] for c in calls if c["path"] == "/api/exchanges/ryft/ryft.dlx/publish"]
assert len(back) == 3, calls
for b, m in zip(back, letters):
    assert b["routing_key"] == "scoring" and b["properties"]["headers"] == m["properties"]["headers"], b
    assert b["payload"] == m["payload"] and b["properties"]["message_id"] == m["properties"]["message_id"], b
' || fail "the letters put back (above)"

# notify's letters: refused without --force, before anything is fetched or any cluster is
# reached (also without RABBITMQ_MGMT_URL, the cluster mode); with --force replayed and marked.
for mode in mgmt cluster; do
  rc=0; run "$mode" replay dlq.notify all >/dev/null 2>"$t/err" || rc=$?
  [ "$rc" = 2 ] || fail "replay of notify's letters without --force must exit 2 ($mode, got $rc)"
  grep -q -- '--force' "$t/err" || fail "the refusal does not name --force: $(cat "$t/err")"
  [ ! -s "$FAKE_CALLS" ] || fail "the refused replay called the API: $(cat "$FAKE_CALLS")"
done
rc=0; out=$(run mgmt replay notify --force 2>"$t/err") || rc=$?
[ "$rc" = 0 ] && [ "$out" = "replayed 1, put back 0" ] || fail "notify --force: $rc $out $(cat "$t/err")"
check "$t/dlq.notify.json" '
pubs = [c["data"] for c in calls if c["path"].endswith("/publish")]
assert len(pubs) == 1 and pubs[0]["routing_key"] == "scored" and pubs[0]["properties"]["headers"] == {"x-ryft-replayed": True}, pubs
' || fail "notify's forced replay (above)"

# Usage errors exit 2 before any cluster is reached.
usage_error() { local rc=0; run cluster "$@" >"$t/out" 2>&1 || rc=$?; [ "$rc" = 2 ] || fail "'dlq.sh $*' must exit 2 (got $rc)"; }
usage_error frob; usage_error show; usage_error replay elsewhere; usage_error purge; usage_error purge dlq.other
usage_error frob
grep -q 'scripts/dlq.sh replay <queue>' "$t/out" || fail "the usage is not printed: $(cat "$t/out")"
[ ! -e "$t/kubectl.called" ] || fail "kubectl ran: $(cat "$t/kubectl.called")"

# list: the letters stay; a letter that was replayed before says so (only the boolean counts).
rc=0; out=$(run mgmt list 2>"$t/err") || rc=$?
[ "$rc" = 0 ] || fail "list exited $rc: $(cat "$t/err")"
echo "$out" | python3 -c '
import json, sys
lines = sys.stdin.read().splitlines()
assert lines[0] == "dlq.scoring: 3" and lines[4] == "dlq.notify: 1", lines
rows = [json.loads(l) for l in lines[1:4]]
assert [r["replayed_before"] for r in rows] == [False, True, False], rows
assert all(r["subject"] == "ingested" and r["attempts"] == "6" and r["dead_lettered_by_broker"] for r in rows), rows
' || fail "list printed: $out"
check "$t/dlq.scoring.json" '
assert {c["data"]["ackmode"] for c in calls if c["path"].endswith("/get")} == {"ack_requeue_true"}, calls
assert not [c for c in calls if c["path"].endswith("/publish")], calls
' || fail "list must leave the letters in place (above)"

echo "✓ dlq_test: replay marks and strips, unrouted letters go back as they were, notify needs --force, usage errors reach no cluster, list shows earlier replays"

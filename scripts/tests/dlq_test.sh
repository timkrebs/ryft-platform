#!/usr/bin/env bash
# dlq.sh against a fake management API and a fake kubectl (make test-scripts); nothing here
# reaches a cluster or a broker (KUBECONFIG=/dev/null, the fakes first on PATH). It checks:
# every argument is checked before kubectl or curl runs; the cluster is named (KUBE_CONTEXT or
# --context) and every cluster call carries it; credentials and bodies stay out of argv; a
# replay marks each letter (x-ryft-replayed: true, a redelivery to its consumer) and strips
# the Ryft-*, death and attempt headers, republishes only the keys its consumer is bound for,
# puts the rest back unchanged, and loses no letter when the API fails half-way. The real
# broker's side is amqpbus's TestReplayedLetterIsARedelivery.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'pkill -f "dlq-test-listener $t" 2>/dev/null || true; rm -rf "$t"' EXIT
mkdir "$t/bin" "$t/tmp"
fail() { echo "✗ dlq_test: $*" >&2; exit 1; }
export KUBECONFIG=/dev/null RABBITMQ_VHOST=ryft FAKE_DIR=$t TMPDIR=$t/tmp
unset KUBE_CONTEXT RABBITMQ_MGMT_URL RABBITMQ_USER RABBITMQ_PASSWORD
PW=pw$RANDOM$RANDOM$RANDOM  # the broker user's password, made up at run time

# kubectl: two contexts; the broker's two Secrets; a port-forward that listens on its port.
cat > "$t/bin/kubectl" <<'K'
#!/bin/sh
echo "$*" >> "$FAKE_DIR/kubectl.calls"
case "$*" in
  "config get-contexts -o name") printf '%s\n' test-ctx kind-ryft ;;
  *" -n ryft-rabbitmq get secret rabbitmq-tls -o jsonpath={.data.ca\.crt}") printf '%s' ZmFrZS1jYQ== ;;
  *" -n ryft-rabbitmq get secret rabbitmq-default-user -o json") cat "$FAKE_DIR/user-secret.json" ;;
  *" -n ryft-rabbitmq port-forward svc/rabbitmq "*":15671")
    port=$(printf '%s\n' "$@" | sed -n 's/^\([0-9]*\):15671$/\1/p')
    exec python3 -c 'import socket, sys, time
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("127.0.0.1", int(sys.argv[1]))); s.listen(8)
s.settimeout(1)
end = time.time() + 60
while time.time() < end:
    try:
        s.accept()[0].close()
    except OSError:
        pass' "$port" "dlq-test-listener $FAKE_DIR" ;;
  *) echo "fake kubectl: unexpected: $*" >&2; exit 3 ;;
esac
K
# curl: the management API. Records each call (argv, method, path, the body from its file, the
# curl config's mode and user) and answers /get from <queue>.json, a publish to ryft.events with
# FAKE_ROUTED (default true), any other publish routed, compact as RabbitMQ writes it. FAKE_FAIL
# (call numbers) and FAKE_FAIL_FROM (a call number) make calls fail as a dropped connection does.
cat > "$t/bin/curl" <<'C'
#!/usr/bin/env python3
import json, os, re, stat, sys
from urllib.parse import urlsplit
d = os.environ["FAKE_DIR"]
counter = os.path.join(d, "curl.n")
n = int(open(counter).read()) + 1 if os.path.exists(counter) else 1
open(counter, "w").write(str(n))
args = sys.argv[1:]
rec = {"n": n, "argv": args, "method": "GET", "data": None}
for i, a in enumerate(args):
    if a == "-X": rec["method"] = args[i + 1]
    if a == "--data-binary": rec["data"] = json.load(open(args[i + 1][1:]))
    if a == "--cacert": rec["cacert"] = open(args[i + 1]).read()
    if a == "-K":
        conf = args[i + 1]
        rec["conf_mode"] = oct(stat.S_IMODE(os.stat(conf).st_mode))
        user = re.search(r'^user = "(.*)"$', open(conf).read(), re.M).group(1)
        rec["user"], rec["password_ok"] = user.split(":")[0], user.split(":", 1)[1] == os.environ.get("FAKE_PW")
rec["path"] = path = urlsplit(args[-1]).path
with open(os.path.join(d, "curl.calls"), "a") as f:
    f.write(json.dumps(rec) + "\n")
if str(n) in os.environ.get("FAKE_FAIL", "").split() or n >= int(os.environ.get("FAKE_FAIL_FROM") or 10**9):
    sys.stderr.write("curl: (7) Failed to connect\n"); sys.exit(7)
if path.endswith("/get"):
    sys.stdout.write("not json" if os.environ.get("FAKE_BAD_TAKE") else "[]" if os.environ.get("FAKE_EMPTY")
                     else open(os.path.join(d, path.split("/")[-2] + ".json")).read())
elif path.endswith("/ryft.events/publish"):
    print('{"routed":%s}' % ("false" if os.environ.get("FAKE_ROUTED") == "false" else "true"))
elif path.endswith("/publish"):
    print('{"routed":true}')
C
chmod +x "$t/bin/kubectl" "$t/bin/curl"

# Letters as the management API's /get returns them, built here: three to replay (one after six
# failed attempts with the tiers' death headers and a header of its own, one replayed before,
# one with a malformed marker), one whose type is not its key (forged: the subject is the routing
# key, amqpbus subjectOf) and one whose key scoring's consumer is not bound for.
FAKE_PW=$PW python3 - "$t" <<'PY'
import base64, json, os, sys
d = sys.argv[1]
death = {"x-ryft-attempt": 5, "x-death": [{"count": 1, "exchange": "ryft.retry", "queue": "scoring.retry.5m",
         "reason": "expired", "routing-keys": ["scoring.retry.5m"], "time": 1761200000}],
         "x-first-death-exchange": "ryft.retry", "x-first-death-queue": "scoring.retry.5s", "x-first-death-reason": "expired",
         "x-last-death-exchange": "ryft.retry", "x-last-death-queue": "scoring.retry.5m", "x-last-death-reason": "expired"}
def letter(mid, subject, typ, consumer, extra):
    h = {"Ryft-Subject": subject, "Ryft-Consumer": consumer, "Ryft-Deliveries": "6", "Ryft-Error": "day 2026-10-20: boom"}
    h.update(death); h.update(extra)
    body = json.dumps({"user_id": "00000000-0000-0000-0000-000000000001", "days": ["2026-10-20"], "reason": "sync"})
    return {"payload_bytes": len(body), "redelivered": False, "exchange": "ryft.dlx", "routing_key": consumer, "message_count": 0,
            "properties": {"message_id": mid, "type": typ, "delivery_mode": 2, "content_type": "application/json", "headers": h},
            "payload": body, "payload_encoding": "string"}
json.dump([letter("first", "ingested", "ingested", "scoring", {"trace": "kept"}),
           letter("again", "ingested", "ingested", "scoring", {"x-ryft-replayed": True}),
           letter("odd", "session.completed", "session.completed", "scoring", {"x-ryft-replayed": "yes"}),
           letter("forged", "ingested", "scored", "scoring", {}),
           letter("unbound", "scored", "scored", "scoring", {})], open(os.path.join(d, "dlq.scoring.json"), "w"))
json.dump([letter("push", "scored", "scored", "notify", {})], open(os.path.join(d, "dlq.notify.json"), "w"))
b64 = lambda s: base64.b64encode(s.encode()).decode()
json.dump({"kind": "Secret", "data": {"username": b64("dlq-user"), "password": b64(os.environ["FAKE_PW"])}},
          open(os.path.join(d, "user-secret.json"), "w"))
PY
export FAKE_PW=$PW

run() { # mgmt|cluster <args>: dlq.sh with the fakes; mgmt against the fake API, cluster through the fake kubectl
  rm -f "$t/kubectl.calls" "$t/curl.calls" "$t/curl.n"; touch "$t/kubectl.calls" "$t/curl.calls"
  local mode=$1; shift
  if [ "$mode" = mgmt ]; then
    PATH="$t/bin:$PATH" RABBITMQ_MGMT_URL=http://dlq-test.invalid RABBITMQ_USER=dlq-user RABBITMQ_PASSWORD=$PW \
      bash "$ROOT/scripts/dlq.sh" "$@"
  else
    PATH="$t/bin:$PATH" bash "$ROOT/scripts/dlq.sh" "$@"
  fi
}
check() { # <python over calls (the curl calls), kcalls (kubectl's), letters (the scoring fixture)>
  python3 -c '
import json, sys
calls = [json.loads(l) for l in open(sys.argv[1])]
kcalls = open(sys.argv[2]).read().splitlines()
letters = json.load(open(sys.argv[3]))
exec(sys.argv[4])' "$t/curl.calls" "$t/kubectl.calls" "$t/dlq.scoring.json" "$1"
}
no_calls() { [ ! -s "$t/kubectl.calls" ] && [ ! -s "$t/curl.calls" ] || fail "$1: called kubectl ($(cat "$t/kubectl.calls")) or curl ($(cat "$t/curl.calls"))"; }
refused() { # <mode> <args>: exit 2, and neither kubectl nor curl ran
  local mode=$1 rc=0; shift
  KUBE_CONTEXT=test-ctx run "$mode" "$@" >"$t/out" 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "'dlq.sh $*' ($mode) must exit 2, got $rc: $(cat "$t/out")"
  no_calls "'dlq.sh $*' ($mode)"
}

# 1. Every argument before any call, in both modes (KUBE_CONTEXT is set: nothing else stops it).
for mode in mgmt cluster; do
  refused "$mode"
  refused "$mode" show scoring x; refused "$mode" replay scoring -1; refused "$mode" show scoring al
  refused "$mode" replay scoring 5O; refused "$mode" replay scoring all --dry-run; refused "$mode" show scoring 0
  refused "$mode" show scoring 1001; refused "$mode" show scoring 01; refused "$mode" list extra; refused "$mode" list --force
  refused "$mode" frob; refused "$mode" show; refused "$mode" replay elsewhere; refused "$mode" purge
  refused "$mode" purge dlq.other; refused "$mode" purge scoring 5; refused "$mode" put-back "$t/none"
  refused "$mode" replay scoring '1, "ackmode": "ack_requeue_true"'; refused "$mode" --context
  refused "$mode" replay dlq.notify all; refused "$mode" replay notify 2
done
grep -q 'notify.s dead letters are not replayed' "$t/out" || fail "the notify refusal does not say why: $(cat "$t/out")"
KUBE_CONTEXT=test-ctx run cluster >"$t/out" 2>&1 || true
head -1 "$t/out" | grep -qx "RabbitMQ's dead letters (dlq.scoring, dlq.notify; D99, docs/OPERATIONS.md → RabbitMQ)" || fail "usage: $(head -2 "$t/out")"
grep -q '^  scripts/dlq.sh replay <queue> \[n|all\]' "$t/out" || fail "usage misses replay: $(cat "$t/out")"
grep -q '^The cluster is named' "$t/out" || fail "usage stops at a blank comment line: $(cat "$t/out")"
if grep -q '^#' "$t/out" || grep -q '^set -euo' "$t/out"; then fail "usage prints code or # prefixes: $(cat "$t/out")"; fi
run mgmt --help > "$t/out" 2>&1 || fail "--help must exit 0"
grep -q '^  scripts/dlq.sh list' "$t/out" || fail "--help: $(cat "$t/out")"

# 2. The cluster is named: without a context nothing runs; a context that is not listed stops at
# the listing; a named one is used for every call, with the Secrets read once each.
rc=0; run cluster list >/dev/null 2>"$t/err" || rc=$?
if [ "$rc" != 2 ] || ! grep -q 'KUBE_CONTEXT' "$t/err"; then fail "no context must exit 2 naming KUBE_CONTEXT (got $rc): $(cat "$t/err")"; fi
no_calls "no context"
rc=0; KUBE_CONTEXT=nope run cluster list >/dev/null 2>"$t/err" || rc=$?
[ "$rc" = 2 ] && [ "$(cat "$t/kubectl.calls")" = "config get-contexts -o name" ] && [ ! -s "$t/curl.calls" ] \
  || fail "an unknown context must stop at the listing (got $rc): $(cat "$t/kubectl.calls")"
rc=0; KUBE_CONTEXT='test-ctx;id' run cluster list >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "a context name with ; must exit 2"; no_calls "a context name with ;"
rc=0; run mgmt --context test-ctx list >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "--context with RABBITMQ_MGMT_URL must exit 2"; no_calls "--context with RABBITMQ_MGMT_URL"
for how in env flag; do
  if [ "$how" = env ]; then out=$(KUBE_CONTEXT=test-ctx run cluster list 2>"$t/err") || fail "cluster list ($how): $(cat "$t/err")"; ctx=test-ctx
  else out=$(KUBE_CONTEXT=test-ctx run cluster list --context kind-ryft 2>"$t/err") || fail "cluster list ($how): $(cat "$t/err")"; ctx=kind-ryft; fi
  grep -qx "→ $ctx" "$t/err" || fail "the context is not announced ($how): $(cat "$t/err")"
  echo "$out" | grep -q '^dlq.scoring: 5$' || fail "cluster list ($how): $out"
  CTX=$ctx check '
import os
ctx = os.environ["CTX"]
assert kcalls[0] == "config get-contexts -o name", kcalls
rest = kcalls[1:]
assert rest and all(c.startswith("--context %s -n ryft-rabbitmq " % ctx) for c in rest), rest
assert sum("get secret rabbitmq-default-user -o json" in c for c in rest) == 1, rest
assert sum("get secret rabbitmq-tls" in c for c in rest) == 1, rest
assert sum(" port-forward svc/rabbitmq " in c for c in rest) == 1, rest
port = [c for c in rest if "port-forward" in c][0].split()[-1].split(":")[0]
assert len(calls) == 2, calls
for c in calls:
    assert c["argv"][-1].startswith("https://rabbitmq.ryft-rabbitmq.svc:%s/api/" % port), c
    assert "rabbitmq.ryft-rabbitmq.svc:%s:127.0.0.1" % port in c["argv"] and c["cacert"] == "fake-ca", c
    assert c["user"] == "dlq-user" and c["password_ok"] and c["conf_mode"] == "0o600", c
' || fail "the cluster calls ($how, above)"
done
grep -rqF -- "$PW" "$t/curl.calls" "$t/kubectl.calls" && fail "the password is in an argv"
sleep 0.5
if pgrep -f "dlq-test-listener $t" >/dev/null; then fail "the port-forward outlived the script"; fi

# 3./4. Replay: each bound letter to ryft.events with its key, marked, stripped, the rest of the
# message as it was; the forged type and the unbound key back into the DLQ unchanged, and named;
# the count and every body in a file, the credentials in a mode-600 config, never in argv.
rc=0; out=$(run mgmt replay dlq.scoring all 2>"$t/err") || rc=$?
[ "$rc" = 1 ] || fail "a replay that put letters back must exit 1 (got $rc): $(cat "$t/err")"
[ "$out" = "replayed 3, put back 2" ] || fail "replay said: $out"
grep -q '! not replayed, put back: message_id "forged", key "ingested": its type "scored" is not its key' "$t/err" \
  || fail "the forged type is not named: $(cat "$t/err")"
grep -q "! not replayed, put back: message_id \"unbound\", key \"scored\": not a key scoring's consumer is bound for" "$t/err" \
  || fail "the unbound key is not named: $(cat "$t/err")"
check '
gets = [c for c in calls if c["path"].endswith("/get")]
assert [g["path"] for g in gets] == ["/api/queues/ryft/dlq.scoring/get"], gets
assert gets[0]["data"] == {"count": 1000, "ackmode": "ack_requeue_false", "encoding": "auto", "truncate": 50000}, gets
pubs = [c for c in calls if c["path"].endswith("/publish")]
assert [p["path"].split("/")[-2] for p in pubs] == ["ryft.events"] * 3 + ["ryft.dlx"] * 2, pubs
death = ("x-death", "x-ryft-attempt", "x-first-death-exchange", "x-first-death-queue", "x-first-death-reason",
         "x-last-death-exchange", "x-last-death-queue", "x-last-death-reason")
for p, m, own in zip(pubs[:3], letters, [{"trace": "kept"}, {}, {}]):
    b, h = p["data"], p["data"]["properties"]["headers"]
    assert h.get("x-ryft-replayed") is True and not [k for k in h if k.startswith("Ryft-") or k in death], h
    assert h == dict(own, **{"x-ryft-replayed": True}), h
    key = m["properties"]["headers"]["Ryft-Subject"]
    assert b["routing_key"] == key and b["payload"] == m["payload"] and b["payload_encoding"] == "string", b
    pr = b["properties"]
    assert (pr["message_id"], pr["type"], pr["delivery_mode"], pr["content_type"]) == (m["properties"]["message_id"], key, 2, "application/json"), pr
for p, m in zip(pubs[3:], letters[3:]):
    b = p["data"]
    assert b["routing_key"] == "scoring" and b["properties"]["headers"] == m["properties"]["headers"], b
    assert b["properties"]["type"] == m["properties"]["type"] and b["payload"] == m["payload"], b
for c in calls:
    assert c["conf_mode"] == "0o600" and c["user"] == "dlq-user" and c["password_ok"], c
    assert not [a for a in c["argv"] if a.startswith("{") or "count" in a], c["argv"]
' || fail "the replay (above)"
grep -qF -- "$PW" "$t/curl.calls" && fail "the password is in an argv"
[ -z "$(ls -A "$t/tmp")" ] || fail "the replay left $(ls -A "$t/tmp")"

# Not routed: every letter back into the DLQ exactly as it was (no marker added), and a failure.
rc=0; out=$(FAKE_ROUTED=false run mgmt replay scoring 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && [ "$out" = "replayed 0, put back 5" ] || fail "unrouted replay: $rc $out $(cat "$t/err")"
check '
back = [c["data"] for c in calls if c["path"] == "/api/exchanges/ryft/ryft.dlx/publish"]
assert len(back) == 5, calls
for b, m in zip(back, letters):
    assert b["routing_key"] == "scoring" and b["properties"]["headers"] == m["properties"]["headers"], b
    assert b["payload"] == m["payload"] and b["properties"]["message_id"] == m["properties"]["message_id"], b
' || fail "the letters put back (above)"

# notify: --force anywhere, never taken as the count; the count reaches the body as a number.
for args in "replay notify --force 2" "replay --force notify 2" "--force replay notify 2"; do
  read -ra words <<< "$args"
  rc=0; out=$(run mgmt "${words[@]}" 2>"$t/err") || rc=$?
  [ "$rc" = 0 ] && [ "$out" = "replayed 1, put back 0" ] || fail "'$args': $rc $out $(cat "$t/err")"
  check '
gets = [c["data"] for c in calls if c["path"] == "/api/queues/ryft/dlq.notify/get"]
assert len(gets) == 1 and gets[0]["count"] == 2, gets
pubs = [c["data"] for c in calls if c["path"].endswith("/publish")]
assert len(pubs) == 1 and pubs[0]["routing_key"] == "scored" and pubs[0]["properties"]["headers"] == {"x-ryft-replayed": True}, pubs
' || fail "'$args' (above)"
done
out=$(run mgmt replay notify --force 2>"$t/err") || fail "replay notify --force: $(cat "$t/err")"
check 'assert [c["data"]["count"] for c in calls if c["path"].endswith("/get")] == [1000], calls' || fail "--force taken as the count"

# 5. Nothing is lost. The connection drops after the first replay: the publish of the second
# letter and its put-back fail, the put-back on the way out too, so the four letters left stay
# in a kept directory (mode 700, no credentials), named on stderr; put-back brings them home.
rc=0; out=$(FAKE_FAIL_FROM=3 run mgmt replay scoring 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && [ "$out" = "replayed 1, put back 0" ] || fail "dropped connection: $rc $out $(cat "$t/err")"
kept=$(sed -n 's/^✗ dlq.sh: kept \([^ ]*\) (mode 700).*/\1/p' "$t/err")
case $kept in "$t/tmp/dlq."?*) ;; *) fail "the kept directory $kept is not in TMPDIR ($t/tmp)" ;; esac
[ -n "$kept" ] && [ -d "$kept/pending" ] || fail "no kept directory named: $(cat "$t/err")"
grep -q "4 letters are in no queue, only in $kept/pending: put them back with scripts/dlq.sh put-back $kept/pending" "$t/err" \
  || fail "what is left is not said: $(cat "$t/err")"
mode=$(stat -c %a "$kept" 2>/dev/null || stat -f %Lp "$kept")
[ "$mode" = 700 ] || fail "the kept directory is mode $mode"
[ "$(find "$kept/pending" -name '*.back.json' -exec basename {} \; | sort | tr '\n' ' ')" = "0002.back.json 0003.back.json 0004.back.json 0005.back.json " ] \
  || fail "kept: $(find "$kept")"
[ ! -e "$kept/curl.conf" ] || fail "the kept directory holds the credentials"
grep -rqF -- "$PW" "$kept" && fail "the kept directory holds the password"
rc=0; out=$(run mgmt put-back "$kept/pending" 2>"$t/err") || rc=$?
[ "$rc" = 0 ] || fail "put-back: $rc $(cat "$t/err")"
check '
back = [c["data"] for c in calls if c["path"] == "/api/exchanges/ryft/ryft.dlx/publish"]
assert [b["properties"]["message_id"] for b in back] == ["again", "odd", "forged", "unbound"], back
for b, m in zip(back, letters[1:]):
    assert b["routing_key"] == "scoring" and b["properties"]["headers"] == m["properties"]["headers"], b
' || fail "put-back (above)"
compgen -G "$kept/pending/*.json" >/dev/null && fail "put-back left $(ls "$kept/pending")"
rm -rf "$kept"
echo '{"routing_key": "ryft.events", "properties": {}}' > "$t/x.back.json"; mkdir -p "$t/odd"; mv "$t/x.back.json" "$t/odd/"
refused mgmt put-back "$t/odd"

# A blip: the second letter's publish and put-back fail, the connection is back for the way
# out, so every letter left is put back and nothing is kept.
rc=0; out=$(FAKE_FAIL="3 4" run mgmt replay scoring 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && [ "$out" = "replayed 1, put back 0" ] || fail "blip: $rc $out $(cat "$t/err")"
grep -q '^put back 4 letters into their dead-letter queue$' "$t/err" || fail "blip: $(cat "$t/err")"
check '
back = [c["data"]["properties"]["message_id"] for c in calls[4:] if c["path"].endswith("/ryft.dlx/publish")]
assert back == ["again", "odd", "forged", "unbound"], back
' || fail "blip (above)"
[ -z "$(ls -A "$t/tmp")" ] || fail "the blip left $(ls -A "$t/tmp")"

# An answer that is not JSON is an error with the raw answer kept, not "empty"; an empty DLQ is.
rc=0; out=$(FAKE_BAD_TAKE=1 run mgmt replay scoring 2>"$t/err") || rc=$?
[ "$rc" = 1 ] || fail "a bad take must fail (got $rc): $out $(cat "$t/err")"
if echo "$out" | grep -q empty; then fail "a bad take is not empty: $out"; fi
kept=$(sed -n 's/^✗ dlq.sh: kept \([^ ]*\) (mode 700).*/\1/p' "$t/err")
case $kept in "$t/tmp/dlq."?*) ;; *) fail "the kept directory $kept is not in TMPDIR ($t/tmp)" ;; esac
[ "$(cat "$kept/taken.json")" = "not json" ] || fail "the raw answer is not kept: $(cat "$t/err")"
rm -rf "$kept"
out=$(FAKE_EMPTY=1 run mgmt replay scoring 2>"$t/err") && [ "$out" = "dlq.scoring is empty" ] || fail "empty: $out $(cat "$t/err")"

# list: the letters stay; a letter that was replayed before says so (only the boolean counts).
out=$(run mgmt list 2>"$t/err") || fail "list: $(cat "$t/err")"
echo "$out" | python3 -c '
import json, sys
lines = sys.stdin.read().splitlines()
assert lines[0] == "dlq.scoring: 5" and lines[6] == "dlq.notify: 1", lines
rows = [json.loads(l) for l in lines[1:6]]
assert [r["replayed_before"] for r in rows] == [False, True, False, False, False], rows
assert all(r["attempts"] == "6" and r["dead_lettered_by_broker"] for r in rows), rows
' || fail "list printed: $out"
check '
assert {c["data"]["ackmode"] for c in calls if c["path"].endswith("/get")} == {"ack_requeue_true"}, calls
assert not [c for c in calls if c["path"].endswith("/publish")], calls
' || fail "list must leave the letters in place (above)"

echo "✓ dlq_test: arguments before any call, a named context on every cluster call, credentials and bodies out of argv, replay marks and strips, only bound keys, nothing lost on a dropped connection, put-back, list"

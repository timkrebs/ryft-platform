#!/usr/bin/env bash
# RabbitMQ's dead letters (dlq.scoring, dlq.notify; D99, docs/OPERATIONS.md → RabbitMQ)
# through the management API:
#
#   scripts/dlq.sh list                    count per queue, then one line per letter (left in place)
#   scripts/dlq.sh show <queue> [n|all]    the first n letters in full (default 10; left in place)
#   scripts/dlq.sh replay <queue> [n|all]  take letters off and publish each to ryft.events with its
#                                          original key, so its consumer handles it again, as a
#                                          redelivery (x-ryft-replayed) with all its attempts
#   scripts/dlq.sh purge <queue>           delete every letter (asks first)
#   scripts/dlq.sh put-back <dir>          the letters a failed replay left in <dir>, back into
#                                          their dead-letter queue
#
# <queue> is dlq.scoring or dlq.notify (or scoring / notify); n is 1 to 1000, all is 1000.
# notify's letters are not replayed without --force: a push hours late is wrong (list and
# purge them). A replay republishes only the keys its queue's consumer is bound for
# (scoring: ingested, session.completed; notify: scored, session.scored, refresh), and only
# a letter whose type is its key; any other goes back into its dead-letter queue as it was,
# and is named. A letter taken off is never dropped: one that is neither replayed nor put
# back is put back on the way out, or else kept in a directory the script names.
#
# The cluster is named, never kubectl's current context (scripts/lib/kube.sh):
#   KUBE_CONTEXT=<context> scripts/dlq.sh list        (or: scripts/dlq.sh --context <context> list)
# It port-forwards to the management API (svc/rabbitmq, 15671), verifies the broker's
# certificate against the internal CA (Secret rabbitmq-tls) and uses the operator's default
# user (Secret rabbitmq-default-user). Elsewhere (compose) no cluster is used:
#   RABBITMQ_MGMT_URL=http://localhost:15672 RABBITMQ_USER=admin RABBITMQ_PASSWORD=… scripts/dlq.sh list
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib/kube.sh
. "$HERE/lib/kube.sh"
MAX=1000

help() { awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; }
usage() { help >&2; exit 2; }
refuse() { echo "✗ dlq.sh: $*" >&2; exit 2; }

# Every argument is checked before anything reaches a cluster or a broker.
force='' ctx='' pos=()
while [ $# -gt 0 ]; do
  case "$1" in
    --force) force=1 ;;
    --context) [ $# -ge 2 ] && [ -n "$2" ] || refuse "--context needs a context name"; ctx=$2; shift ;;
    --context=?*) ctx=${1#--context=} ;;
    -h|--help) help; exit 0 ;;
    -*) echo "✗ dlq.sh: unknown option $1" >&2; usage ;;
    *) pos+=("$1") ;;
  esac
  shift
done
[ "${#pos[@]}" -gt 0 ] || usage
cmd=${pos[0]}
queue_of() {
  case "$1" in
    dlq.scoring|scoring) echo dlq.scoring ;;
    dlq.notify|notify) echo dlq.notify ;;
    *) refuse "the queue is dlq.scoring or dlq.notify, not $1" ;;
  esac
}
count_of() { # n|all → 1..MAX
  if [ "$1" = all ]; then echo $MAX; return; fi
  [[ "$1" =~ ^[1-9][0-9]{0,3}$ ]] && [ "$1" -le $MAX ] || refuse "n is 1 to $MAX or all, not $1"
  echo "$1"
}
q='' n='' dir=''
case "$cmd" in
  list)
    [ "${#pos[@]}" = 1 ] || usage ;;
  show|replay)
    [ "${#pos[@]}" -ge 2 ] && [ "${#pos[@]}" -le 3 ] || usage
    q=$(queue_of "${pos[1]}")
    if [ "${#pos[@]}" = 3 ]; then n=$(count_of "${pos[2]}"); elif [ "$cmd" = show ]; then n=10; else n=$MAX; fi ;;
  purge)
    [ "${#pos[@]}" = 2 ] || usage
    q=$(queue_of "${pos[1]}") ;;
  put-back)
    [ "${#pos[@]}" = 2 ] || usage
    dir=${pos[1]}
    [ -d "$dir" ] || refuse "no directory $dir"
    compgen -G "$dir/*.back.json" >/dev/null || refuse "no letters (*.back.json) in $dir"
    python3 - "$dir" <<'PY' || refuse "$dir holds something that is not a dead letter dlq.sh kept"
import glob, json, os, sys
for f in glob.glob(os.path.join(sys.argv[1], "*.back.json")):
    b = json.load(open(f))
    if b.get("routing_key") not in ("scoring", "notify") or not isinstance(b.get("properties"), dict):
        sys.exit(f)
PY
    ;;
  *) usage ;;
esac
if [ -n "$force" ] && [ "$cmd" != replay ]; then refuse "--force is for a replay of notify's letters"; fi
if [ "$cmd" = replay ] && [ "$q" = dlq.notify ] && [ -z "$force" ]; then
  refuse "notify's dead letters are not replayed (a push hours late is wrong); purge them, or --force"
fi
VHOST=${RABBITMQ_VHOST:-ryft}
[[ "$VHOST" =~ ^[A-Za-z0-9._-]+$ ]] || refuse "RABBITMQ_VHOST $VHOST is not a vhost name"
if [ -n "${RABBITMQ_MGMT_URL:-}" ]; then
  [ -z "$ctx" ] || refuse "--context and RABBITMQ_MGMT_URL exclude each other"
  : "${RABBITMQ_USER:?with RABBITMQ_MGMT_URL set RABBITMQ_USER and RABBITMQ_PASSWORD}" "${RABBITMQ_PASSWORD:?}"
else
  kube_context "$ctx" || exit 2
fi

# The keys each dead-letter queue's consumer is bound for (amqpbus consumerKeys; a Go test
# compares them): a replay republishes nothing else.
replay_keys() {
  case "$1" in
    dlq.scoring) echo "ingested session.completed" ;;
    dlq.notify) echo "scored session.scored refresh" ;;
  esac
}

# Credentials, request bodies and taken letters live in $tmp (mode 700, files 600), never in
# argv or the environment of a child.
umask 077
tmp=$(mktemp -d "${TMPDIR:-/tmp}/dlq.XXXXXX")
chmod 700 "$tmp"
pf='' keep=''
put_back_dir() { # DIR: each *.back.json in it back into its dead-letter queue (until one fails); 1 if any is left
  local f back_n=0 left=0
  for f in "$1"/*.back.json; do
    [ -e "$f" ] || continue
    publish ryft.dlx "$f" || break
    rm -f "$f" "${f%.back.json}.replay.json"
    back_n=$((back_n + 1))
  done
  for f in "$1"/*.back.json; do [ ! -e "$f" ] || left=$((left + 1)); done
  [ "$back_n" = 0 ] || echo "put back $back_n letters into their dead-letter queue" >&2
  [ "$left" = 0 ] || { echo "✗ dlq.sh: $left letters are in no queue, only in $1: put them back with scripts/dlq.sh put-back $1" >&2; return 1; }
}
cleanup() {
  local status=$?
  # Letters taken off and neither replayed nor put back: put back now, or keep them.
  if [ -d "$tmp/pending" ] && compgen -G "$tmp/pending/*.back.json" >/dev/null; then
    [ "$status" != 0 ] || status=1
    put_back_dir "$tmp/pending" || keep=1
  fi
  if [ -n "$pf" ]; then
    pkill -P "$pf" 2>/dev/null || true
    kill "$pf" 2>/dev/null || true
  fi
  if [ -n "$keep" ]; then
    rm -f "$tmp/curl.conf" "$tmp/user.json" "$tmp/ca.b64"
    chmod 700 "$tmp"
    echo "✗ dlq.sh: kept $tmp (mode 700) with what is left: $(cd "$tmp" && find . -type f ! -name ca.crt | sed 's|^\./||' | sort | tr '\n' ' ')" >&2
  else
    rm -rf "$tmp"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The management API's user as a curl config (curl -K), from the environment or the Secret.
write_curl_conf() { # env | secrets
  python3 - "$1" "$tmp" <<'PY'
import base64, json, os, sys
mode, d = sys.argv[1], sys.argv[2]
if mode == "secrets":
    data = json.load(open(os.path.join(d, "user.json")))["data"]
    user, pw = (base64.b64decode(data[k]).decode() for k in ("username", "password"))
    with open(os.path.join(d, "ca.crt"), "wb") as f:
        f.write(base64.b64decode(open(os.path.join(d, "ca.b64")).read()))
else:
    user, pw = os.environ["RABBITMQ_USER"], os.environ["RABBITMQ_PASSWORD"]
def quoted(s):
    for a, b in (("\\", "\\\\"), ('"', '\\"'), ("\n", "\\n"), ("\r", "\\r"), ("\t", "\\t")):
        s = s.replace(a, b)
    return '"' + s + '"'
with open(os.path.join(d, "curl.conf"), "w") as f:
    f.write("user = %s\nconnect-timeout = 10\nmax-time = 120\n" % quoted(user + ":" + pw))
PY
}
if [ -n "${RABBITMQ_MGMT_URL:-}" ]; then
  mgmt=$RABBITMQ_MGMT_URL
  write_curl_conf env
  unset RABBITMQ_USER RABBITMQ_PASSWORD
  CURL_EXTRA=()
else
  ns=ryft-rabbitmq
  k -n $ns get secret rabbitmq-tls -o jsonpath='{.data.ca\.crt}' > "$tmp/ca.b64"
  k -n $ns get secret rabbitmq-default-user -o json > "$tmp/user.json"
  write_curl_conf secrets
  rm -f "$tmp/user.json" "$tmp/ca.b64"
  port=$((20000 + RANDOM % 20000))
  k -n $ns port-forward svc/rabbitmq "$port:15671" >/dev/null 2>&1 &
  pf=$!
  disown "$pf"
  for _ in $(seq 1 30); do (echo > "/dev/tcp/127.0.0.1/$port") 2>/dev/null && break; sleep 0.3; done
  # The certificate names the Service, so connect to that name through the forward.
  mgmt=https://rabbitmq.$ns.svc:$port
  CURL_EXTRA=(--cacert "$tmp/ca.crt" --resolve "rabbitmq.$ns.svc:$port:127.0.0.1")
fi
api() { # METHOD PATH [BODY FILE] → the answer
  curl -fsS -K "$tmp/curl.conf" ${CURL_EXTRA[@]+"${CURL_EXTRA[@]}"} -H 'content-type: application/json' \
    -X "$1" ${3:+--data-binary "@$3"} "$mgmt$2"
}
publish() { # EXCHANGE BODY-FILE: true only when the broker routed it
  local out
  out=$(api POST "/api/exchanges/$VHOST/$1/publish" "$2" 2>/dev/null) && [[ "$out" == *'"routed":true'* ]]
}
get() { # QUEUE COUNT ACKMODE → the letters, a JSON array
  python3 -c 'import json, sys; json.dump({"count": int(sys.argv[1]), "ackmode": sys.argv[2], "encoding": "auto",
    "truncate": 50000}, open(sys.argv[3], "w"))' "$2" "$3" "$tmp/get.json"
  api POST "/api/queues/$VHOST/$1/get" "$tmp/get.json"
}

case "$cmd" in
  list)
    for lq in dlq.scoring dlq.notify; do
      get "$lq" $MAX ack_requeue_true > "$tmp/$lq.json"
      python3 - "$lq" "$tmp/$lq.json" <<'PY'
import json, sys
letters = json.load(open(sys.argv[2]))
print("%s: %d" % (sys.argv[1], len(letters)))
for m in letters:
    p = m.get("properties") or {}; h = p.get("headers") or {}
    print(json.dumps({"subject": h.get("Ryft-Subject"), "attempts": h.get("Ryft-Deliveries"),
        "error": h.get("Ryft-Error"), "message_id": p.get("message_id"), "dead_lettered_by_broker": "x-death" in h,
        "replayed_before": h.get("x-ryft-replayed") is True}))
PY
    done ;;
  show)
    get "$q" "$n" ack_requeue_true | python3 -m json.tool ;;
  replay)
    get "$q" "$n" ack_requeue_false > "$tmp/taken.json" || {
      keep=1; echo "✗ dlq.sh: the take from $q failed: letters it took may be only in $tmp/taken.json" >&2; exit 1; }
    # Per letter, written at once (pending/ appears only when all are): the publish to
    # ryft.events with its key, the one back into its dead-letter queue, and the verdict.
    python3 - "$tmp/taken.json" "${q#dlq.}" "$(replay_keys "$q")" "$tmp/pending" <<'PY' || {
import json, os, sys
taken, consumer, keys, out = json.load(open(sys.argv[1])), sys.argv[2], sys.argv[3].split(), sys.argv[4]
if not isinstance(taken, list):
    sys.exit("the take did not answer a list")
death = ("x-death", "x-ryft-attempt", "x-first-death-exchange", "x-first-death-queue", "x-first-death-reason",
         "x-last-death-exchange", "x-last-death-queue", "x-last-death-reason")
# amqpbus.HeaderReplayed: the consumer takes the letter as a redelivery (events.Redelivered), so
# scoring finishes a re-score its failed attempts left half-done (D112). Only the boolean true
# counts. Without x-ryft-attempt the letter has all its attempts again.
REPLAYED = "x-ryft-replayed"
new = out + ".new"
os.mkdir(new)
manifest = []
for i, m in enumerate(taken, 1):
    p = m.get("properties") or {}
    h = dict(p.get("headers") or {})
    key = h.get("Ryft-Subject") or p.get("type") or ""
    typ = p.get("type") or key
    def body(k, headers):
        return {"properties": {"content_type": p.get("content_type", "application/json"), "delivery_mode": 2,
                "message_id": p.get("message_id", ""), "type": p.get("type", key), "headers": headers},
                "routing_key": k, "payload": m["payload"], "payload_encoding": m.get("payload_encoding", "string")}
    name = "%04d" % i
    with open(os.path.join(new, name + ".back.json"), "w") as f:
        json.dump(body(consumer, h), f)
    what = "message_id %s, key %s" % (json.dumps(p.get("message_id", "")), json.dumps(key))
    if key not in keys:
        verdict, what = "refused", what + ": not a key %s's consumer is bound for" % consumer
    elif typ != key:
        verdict, what = "refused", what + ": its type %s is not its key" % json.dumps(typ)
    else:
        verdict = "replay"
        clean = {k: v for k, v in h.items() if not k.startswith("Ryft-") and k not in death}
        clean[REPLAYED] = True
        with open(os.path.join(new, name + ".replay.json"), "w") as f:
            json.dump(body(key, clean), f)
    manifest.append("\t".join((name, verdict, what)))
with open(os.path.join(new, "manifest.tsv"), "w") as f:
    f.write("".join(l + "\n" for l in manifest))
os.rename(new, out)
PY
      keep=1; echo "✗ dlq.sh: the letters taken from $q could not be read: they are only in $tmp/taken.json" >&2; exit 1; }
    rm -f "$tmp/taken.json"
    [ -s "$tmp/pending/manifest.tsv" ] || { echo "$q is empty"; exit 0; }
    ok=0 back=0 failed=
    while IFS=$'\t' read -r name verdict what <&3; do
      if [ "$verdict" = replay ] && publish ryft.events "$tmp/pending/$name.replay.json"; then
        rm -f "$tmp/pending/$name.back.json" "$tmp/pending/$name.replay.json"
        ok=$((ok + 1))
      elif publish ryft.dlx "$tmp/pending/$name.back.json"; then
        rm -f "$tmp/pending/$name.back.json" "$tmp/pending/$name.replay.json"
        back=$((back + 1))
        if [ "$verdict" = replay ]; then echo "! not routed, put back: $what" >&2; else echo "! not replayed, put back: $what" >&2; fi
      else
        echo "✗ dlq.sh: neither replayed nor put back: $what" >&2
        failed=1
        break
      fi
    done 3< "$tmp/pending/manifest.tsv"
    echo "replayed $ok, put back $back"
    [ -z "$failed" ] && [ "$back" = 0 ] ;;
  purge)
    n=$(get "$q" $MAX ack_requeue_true | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
    read -r -p "delete all $n letters in $q? type PURGE: " answer
    [ "$answer" = PURGE ] || { echo "nothing deleted"; exit 1; }
    api DELETE "/api/queues/$VHOST/$q/contents" && echo "$q purged" ;;
  put-back)
    put_back_dir "$dir" ;;
esac

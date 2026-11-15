#!/usr/bin/env bash
# dlq.sh against a fake gcloud (make test-scripts). Nothing here reaches Google, a cluster or an emulator:
# the fake is first on PATH (checked), CLOUDSDK_CONFIG is the test's own directory, and a fake kubectl
# records any call (there must be none). The fake keeps each hold subscription's letters, their leases and
# their acks. It checks: every argument before any gcloud call; --project required and on every call; the
# regional endpoint, or the emulator's without credentials; list and show pull without --auto-ack, ack
# nothing and hand every letter back; a replay republishes only the keys the consumer handles, under the
# key its source subscription (or the consumer's own filing) gives, to that topic, with ryft-replayed=true,
# its own attributes minus Pub/Sub's and ryft-error, the ordering key on an ordered topic and the data as it
# was (those three in a mode-600 flags file, never on argv, with gcloud's file log off), acks each letter only
# after its publish, and refuses a foreign key and leaves the letter; a failed
# publish or ack stops the run and acks nothing; notify's letters need --force; purge asks, then seeks.
# The keys and topics come from infra/gcp/pubsub.auto.tfvars.json, so this also pins dlq.sh to topology.go.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir "$t/bin" "$t/tmp" "$t/store" "$t/fixtures" "$t/gcloud-config"
fail() { echo "✗ dlq_test: $*" >&2; exit 1; }
export FAKE_DIR=$t TMPDIR=$t/tmp CLOUDSDK_CONFIG=$t/gcloud-config
unset PUBSUB_EMULATOR_HOST CLOUDSDK_API_ENDPOINT_OVERRIDES_PUBSUB CLOUDSDK_AUTH_DISABLE_CREDENTIALS FAKE_FAIL FAKE_GARBAGE
P=ryft-test-proj
U1=00000000-0000-0000-0000-000000000001
U2=00000000-0000-0000-0000-000000000002

# gcloud: pubsub subscriptions pull|ack|modify-message-ack-deadline|seek and topics publish against
# store/<subscription>.json (the letters) and store/<subscription>.leased (the ids pulled and neither acked
# nor handed back: a pull skips them, as Pub/Sub does during a lease). Each call is a line of calls.jsonl:
# its number, argv, the endpoint, credential and file-log settings it saw, and the --flags-file it was given
# (its JSON, and whether it was mode 600 inside the script's own temporary directory). FAKE_FAIL="<verb>"
# fails that verb ("pubsub topics publish", ...); FAKE_GARBAGE makes a pull answer something that is not JSON.
cat > "$t/bin/gcloud" <<'G'
#!/usr/bin/env bash
set -euo pipefail
d=$FAKE_DIR
n=$(( $(cat "$d/n" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$d/n"
ff='' ffjson=null private=false
for a in "$@"; do case "$a" in --flags-file=*) ff=${a#--flags-file=} ;; esac; done
if [ -n "$ff" ]; then
  ffjson=$(jq -c . "$ff")
  case "$ff" in
    "$d"/tmp/dlq.*/*) if [ -n "$(find "$ff" -prune -perm 0600)" ]; then private=true; fi ;;
  esac
fi
printf '%s\n' "$@" | jq -Rsc --argjson n "$n" --arg ep "${CLOUDSDK_API_ENDPOINT_OVERRIDES_PUBSUB-}" \
  --arg nocred "${CLOUDSDK_AUTH_DISABLE_CREDENTIALS-}" --arg nolog "${CLOUDSDK_CORE_DISABLE_FILE_LOGGING-}" \
  --argjson ff "$ffjson" --argjson private "$private" \
  '{n: $n, argv: (split("\n") | .[:-1]), endpoint: $ep, nocred: $nocred, nolog: $nolog, flags_file: $ff, flags_private: $private}' >> "$d/calls.jsonl"
pos=() limit=0 ids='' deadline=''
for a in "$@"; do
  case "$a" in
    --limit=*) limit=${a#--limit=} ;;
    --ack-ids=*) ids=${a#--ack-ids=} ;;
    --ack-deadline=*) deadline=${a#--ack-deadline=} ;;
    -*) ;;
    *) pos+=("$a") ;;
  esac
done
verb="${pos[0]-} ${pos[1]-} ${pos[2]-}"
if [ "${FAKE_FAIL:-}" = "$verb" ]; then echo "ERROR: (gcloud) UNAVAILABLE: the fake fails $verb" >&2; exit 1; fi
sub=${pos[3]-}
store=$d/store/$sub.json leased=$d/store/$sub.leased
drop() { { grep -vxF -- "$2" "$1" || true; } > "$1.new"; mv "$1.new" "$1"; }
case "$verb" in
  "pubsub subscriptions pull")
    [ -f "$store" ] || { echo "ERROR: (gcloud.pubsub.subscriptions.pull) NOT_FOUND: $sub" >&2; exit 1; }
    [ -z "${FAKE_GARBAGE:-}" ] || { echo "not json"; exit 0; }
    touch "$leased"
    jq -c --rawfile l "$leased" --argjson lim "$limit" --arg n "$n" \
      '[.[] | select(.message.messageId as $id | any($l | split("\n")[]; . == $id) | not)] | .[:$lim]
       | map(. + {ackId: ("ack." + $n + "." + .message.messageId)})' "$store" > "$d/pulled.json"
    jq -r '.[].message.messageId' "$d/pulled.json" >> "$leased"
    cat "$d/pulled.json" ;;
  "pubsub subscriptions ack")
    touch "$leased"
    IFS=, read -ra list <<< "$ids"
    for id in "${list[@]}"; do
      jq -c --arg m "${id##*.}" 'map(select(.message.messageId != $m))' "$store" > "$store.new"; mv "$store.new" "$store"
      drop "$leased" "${id##*.}"
    done ;;
  "pubsub subscriptions modify-message-ack-deadline")
    touch "$leased"
    if [ "$deadline" = 0 ]; then
      IFS=, read -ra list <<< "$ids"
      for id in "${list[@]}"; do drop "$leased" "${id##*.}"; done
    fi ;;
  "pubsub subscriptions seek")
    echo '[]' > "$store"; : > "$leased" ;;
  "pubsub topics publish")
    echo "{\"messageIds\": [\"pub-$n\"]}" ;;
  *) echo "fake gcloud: unexpected: $*" >&2; exit 3 ;;
esac
G
cat > "$t/bin/kubectl" <<'K'
#!/bin/sh
echo "$*" >> "$FAKE_DIR/kubectl.calls"
exit 3
K
chmod +x "$t/bin/gcloud" "$t/bin/kubectl"
[ "$(PATH="$t/bin:$PATH" command -v gcloud)" = "$t/bin/gcloud" ] || fail "the fake gcloud is not first on PATH"

# The letters, as `gcloud pubsub subscriptions pull --format=json` returns them (without the ackId, which
# the fake adds per pull). scoring's hold: three to replay (L1 dead-lettered by Pub/Sub from scoring-ingested,
# with a ryft-error its publisher forged and an attribute of its own; L2 from scoring-session-completed by
# its full name, no ryft-key; L3 filed by the consumer itself), and six to refuse: a ryft-key that is not
# its subscription's (L4), a key scoring does not handle (L5), another consumer's subscription (L6),
# neither Pub/Sub's nor the consumer's (L7), an ordered topic's event without a user_id (L8), another
# project (L9). notify's hold: two to replay (N1 from notify-wait, N2 from notify-refresh), two to refuse.
jq -n --arg u1 "$U1" --arg u2 "$U2" '
def body($u): {user_id: $u, days: ["2026-11-14"], reason: "sync"} | tojson;
def src($s): {CloudPubSubDeadLetterSourceSubscription: $s, CloudPubSubDeadLetterSourceSubscriptionProject: "ryft-test-proj",
              CloudPubSubDeadLetterSourceDeliveryCount: "20", CloudPubSubDeadLetterSourceTopicPublishTime: "2026-11-15T07:00:00Z"};
def letter($id; $a; $d): {message: {messageId: $id, publishTime: "2026-11-15T08:00:00Z", attributes: $a, data: ($d | @base64)}};
{scoring: [
   letter("L1"; src("scoring-ingested") + {"ryft-key": "ingested", "ryft-message-id": "m1", "ryft-reason": "sync",
     "ryft-error": "forged by the publisher", trace: "kept"}; body($u1)),
   letter("L2"; src("projects/ryft-test-proj/subscriptions/scoring-session-completed") + {"ryft-message-id": "m2"}; body($u2)),
   letter("L3"; {"ryft-key": "ingested", "ryft-message-id": "m3", "ryft-error": "decode: unexpected end of JSON input"}; body($u1)),
   letter("L4"; src("scoring-ingested") + {"ryft-key": "session.completed", "ryft-message-id": "m4"}; body($u1)),
   letter("L5"; {"ryft-key": "scored", "ryft-message-id": "m5", "ryft-error": "bad"}; body($u1)),
   letter("L6"; src("notify-scored") + {"ryft-message-id": "m6"}; body($u1)),
   letter("L7"; {"ryft-key": "ingested", "ryft-message-id": "m7"}; body($u1)),
   letter("L8"; src("scoring-ingested") + {"ryft-message-id": "m8"}; ({days: ["2026-11-14"]} | tojson)),
   letter("L9"; src("scoring-ingested") + {"ryft-message-id": "m9", CloudPubSubDeadLetterSourceSubscriptionProject: "ryftlab-prod"}; body($u1))],
 notify: [
   letter("N1"; src("notify-wait") + {"ryft-key": "scored", "ryft-message-id": "n1", "ryft-reason": "sync"}; ({user_id: $u1, day: "2026-11-14"} | tojson)),
   letter("N2"; src("notify-refresh") + {"ryft-message-id": "n2"}; ({user_id: $u1} | tojson)),
   letter("N3"; src("notify-wait") + {"ryft-key": "ingested", "ryft-message-id": "n3"}; body($u1)),
   letter("N4"; {"ryft-message-id": "n4", "ryft-error": "no ryft-key on /push/wait"}; body($u1))]}' > "$t/fixtures/all.json"
jq '.scoring' "$t/fixtures/all.json" > "$t/fixtures/dlq-scoring-hold.json"
jq '.notify' "$t/fixtures/all.json" > "$t/fixtures/dlq-notify-hold.json"

reset() { # the fixtures back in the store, no leases, no calls
  [ ! -s "$t/calls.jsonl" ] || cat "$t/calls.jsonl" >> "$t/all-calls.jsonl"
  rm -f "$t/store/"* "$t/n"; : > "$t/calls.jsonl"
  cp "$t/fixtures/dlq-scoring-hold.json" "$t/fixtures/dlq-notify-hold.json" "$t/store/"
}
run() { PATH="$t/bin:$PATH" bash "$ROOT/scripts/dlq.sh" "$@"; }
# calls <jq condition> <what>: the condition over the run's calls (an array), with the helpers below and
# the fixtures as $fx; prints the calls when it does not hold. flag and has_flag read argv and the flags file
# (as gcloud does); on_argv reads argv only.
JQ='def cmd: .argv[0:3] | join(" ");
def flag($k): ([.argv[] | select(startswith("--" + $k + "=")) | ltrimstr("--" + $k + "=")] + [(.flags_file // {})["--" + $k] // empty])[0];
def on_argv($k): any(.argv[]; . == "--" + $k or startswith("--" + $k + "="));
def has_flag($k): on_argv($k) or ((.flags_file // {}) | has("--" + $k));
def ids: flag("ack-ids") | split(",") | map(sub("^ack\\.[0-9]+\\."; ""));
def attrs: flag("attribute") | capture("^\\^(?<d>.)\\^(?<r>.*)$"; "s") as $m
  | [$m.r | split($m.d)[] | capture("^(?<k>[^=]+)=(?<v>.*)$"; "s") | {(.k): .v}] | add;
def pubs: [.[] | select(cmd == "pubsub topics publish")];
def acks: [.[] | select(cmd == "pubsub subscriptions ack") | ids[]];
def released: [.[] | select(cmd == "pubsub subscriptions modify-message-ack-deadline" and flag("ack-deadline") == "0") | ids[]];
def body($set; $i): $fx[0][$set][$i].message.data | @base64d;
def letter_off_argv: all(pubs[]; ((on_argv("message") or on_argv("attribute") or on_argv("ordering-key")) | not)
    and .flags_private and ((.flags_file | keys) - ["--attribute", "--message", "--ordering-key"]) == [])
  and all(.[]; all(.argv[]; (contains($u1) or contains($u2) or contains("ryft-message-id")) | not));
'
calls() {
  jq -e -s --slurpfile fx "$t/fixtures/all.json" --arg u1 "$U1" --arg u2 "$U2" --arg p "$P" "$JQ $1" "$t/calls.jsonl" >/dev/null \
    || { cat "$t/calls.jsonl" >&2; fail "$2 (the calls above)"; }
}
left() { # <hold> <ids...>: what the hold subscription still holds, and nothing leased
  local h=$1; shift
  jq -e '[.[].message.messageId] == $ARGS.positional' "$t/store/$h.json" --args "$@" >/dev/null \
    || fail "$h holds $(jq -c '[.[].message.messageId]' "$t/store/$h.json"), not $*"
  [ ! -s "$t/store/$h.leased" ] || fail "$h still has leased letters: $(tr '\n' ' ' < "$t/store/$h.leased")"
}
clean() { [ -z "$(ls -A "$t/tmp")" ] || fail "$1 left $(ls -A "$t/tmp")"; }
refused() { # <args>: exit 2, and gcloud never ran
  local rc=0; reset
  run "$@" >"$t/out" 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "'dlq.sh $*' must exit 2, got $rc: $(cat "$t/out")"
  [ ! -s "$t/calls.jsonl" ] || fail "'dlq.sh $*' called gcloud: $(cat "$t/calls.jsonl")"
}

# 1. Every argument before any call; the project is required and well-formed.
refused; refused list; refused --project "$P"; refused --project "$P" frob; refused --project "$P" list extra
refused --project "$P" list --force; refused --project "$P" show; refused --project "$P" show elsewhere
refused --project "$P" show scoring x; refused --project "$P" show scoring 0; refused --project "$P" show scoring 01
refused --project "$P" show scoring 1001; refused --project "$P" show scoring al; refused --project "$P" replay scoring -1
refused --project "$P" replay scoring 5O; refused --project "$P" replay scoring all --dry-run; refused --project "$P" replay scoring 1 2
refused --project "$P" purge; refused --project "$P" purge scoring 5; refused --project "$P" purge dlq-other-hold
refused --project "$P" --force list; refused --project; refused --project= list; refused --context ryft-prod-euc1 list
refused --project Ryft-Prod list; refused --project 'ryft-test-proj;id' list; refused --project "$P" replay dlq.scoring
PUBSUB_EMULATOR_HOST=localhost refused --project "$P" list
refused list scoring --project "$P"
refused --project "$P" replay notify
grep -q "notify's dead letters are not replayed" "$t/out" || fail "the notify refusal does not say why: $(cat "$t/out")"
refused --project "$P" replay dlq-notify-hold 2
refused list
grep -q 'name the project: --project <id> (there is no default' "$t/out" || fail "no project: $(cat "$t/out")"
rc=0; run > "$t/out" 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "no arguments must exit 2, got $rc"
head -1 "$t/out" | grep -q '^The dead letters on Pub/Sub ' || fail "usage: $(head -2 "$t/out")"
grep -qF '  scripts/dlq.sh --project <id> replay <consumer> [n|all] [--force]' "$t/out" || fail "usage misses replay: $(cat "$t/out")"
grep -q '^A replay republishes only the keys' "$t/out" || fail "usage stops at a blank comment line: $(cat "$t/out")"
if grep -q '^#' "$t/out" || grep -q '^set -euo' "$t/out"; then fail "usage prints code or # prefixes: $(cat "$t/out")"; fi
run --help > "$t/out" 2>&1 || fail "--help must exit 0"
grep -qF '  scripts/dlq.sh --project <id> list' "$t/out" || fail "--help: $(cat "$t/out")"

# 2. list: both hold subscriptions, a line per letter with its verdict; nothing acked, no --auto-ack,
# every letter handed back; the project and the regional endpoint on every call.
reset
out=$(run --project "$P" list 2>"$t/err") || fail "list: $(cat "$t/err")"
grep -qx "→ project $P, https://europe-west1-pubsub.googleapis.com/" "$t/err" || fail "the project is not announced: $(cat "$t/err")"
[ "$(sed -n 1p <<<"$out")" = "dlq-scoring-hold: 9" ] && [ "$(sed -n 11p <<<"$out")" = "dlq-notify-hold: 4" ] || fail "list printed: $out"
sed -n '2,10p' <<<"$out" | jq -e -s '
  [.[].letter_id] == ["L1","L2","L3","L4","L5","L6","L7","L8","L9"]
  and [.[].verdict] == ["replay","replay","replay","refused","refused","refused","refused","refused","refused"]
  and [.[].by] == ["pubsub","pubsub","consumer","pubsub","consumer","pubsub","unknown","pubsub","pubsub"]
  and [.[].key] == ["ingested","session.completed","ingested","session.completed","scored",null,"ingested",null,null]
  and .[0].attempts == "20" and .[0].error == null and .[0].source == "scoring-ingested" and .[0].message_id == "m1"
  and .[1].source == "scoring-session-completed" and .[2].error == "decode: unexpected end of JSON input"
  and all(.[]; .replayed_before == false and (has("data") | not))' >/dev/null || fail "list's scoring lines: $out"
sed -n '12,15p' <<<"$out" | jq -e -s '[.[].verdict] == ["replay","replay","refused","refused"] and [.[].key] == ["scored","refresh","ingested",null]' >/dev/null \
  || fail "list's notify lines: $out"
calls 'all(.[]; .argv[-2:] == ["--project=" + $p, "--quiet"] and .endpoint == "https://europe-west1-pubsub.googleapis.com/" and .nocred == "")
  and ([.[] | select(cmd == "pubsub subscriptions pull") | .argv[3]] | unique) == ["dlq-notify-hold", "dlq-scoring-hold"]
  and all(.[] | select(cmd == "pubsub subscriptions pull"); flag("format") == "json" and (has_flag("auto-ack") | not))
  and acks == [] and pubs == [] and ([.[] | select(cmd == "pubsub subscriptions seek")] == [])
  and (released | sort) == ["L1","L2","L3","L4","L5","L6","L7","L8","L9","N1","N2","N3","N4"]' "list acks nothing and hands every letter back"
left dlq-scoring-hold L1 L2 L3 L4 L5 L6 L7 L8 L9; left dlq-notify-hold N1 N2 N3 N4; clean list
# An operator's CLOUDSDK_AUTH_DISABLE_CREDENTIALS never reaches a production call.
reset; CLOUDSDK_AUTH_DISABLE_CREDENTIALS=true run --project "$P" list >/dev/null 2>&1 || fail "list with credentials disabled"
calls 'all(.[]; .nocred == "")' "CLOUDSDK_AUTH_DISABLE_CREDENTIALS reached gcloud without the emulator"

# 3. show: n letters in full, the data decoded; nothing acked.
reset
out=$(run --project "$P" show scoring 2 2>"$t/err") || fail "show: $(cat "$t/err")"
jq -e --arg u1 "$U1" 'length == 2 and .[0].data.user_id == $u1 and .[0].attributes["ryft-error"] == "forged by the publisher"
  and .[0].verdict == "replay" and .[1].letter_id == "L2"' <<<"$out" >/dev/null || fail "show printed: $out"
calls '[.[] | select(cmd == "pubsub subscriptions pull") | flag("limit")] == ["2"] and acks == [] and pubs == [] and (released | sort) == ["L1","L2"]' "show"
left dlq-scoring-hold L1 L2 L3 L4 L5 L6 L7 L8 L9; clean show

# 4. replay: L1-L3 to their topics, marked, with their own attributes and data, the ordering key, each
# acked after its publish; the six others refused, named and left.
reset; rc=0
out=$(run --project "$P" replay scoring all 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && [ "$out" = "replayed 3, refused 6" ] || fail "replay scoring: $rc $out $(cat "$t/err")"
for want in \
  'message_id "m4" (letter L4), key "session.completed", dead-lettered by Pub/Sub from scoring-ingested: its ryft-key "session.completed" is not the key of scoring-ingested (ingested)' \
  'message_id "m5" (letter L5), key "scored", filed by the consumer: its ryft-key "scored" is not a key scoring handles' \
  "message_id \"m6\" (letter L6), key null, dead-lettered by Pub/Sub from notify-scored: dead-lettered from \"notify-scored\", which is not one of scoring's subscriptions" \
  'message_id "m7" (letter L7), key "ingested", of unknown origin: neither dead-lettered by Pub/Sub' \
  "message_id \"m8\" (letter L8), key null, dead-lettered by Pub/Sub from scoring-ingested: ryft-ingested is ordered by the event's user_id, and its data has none" \
  'message_id "m9" (letter L9), key null, dead-lettered by Pub/Sub from scoring-ingested: dead-lettered in project "ryftlab-prod", not ryft-test-proj'; do
  grep -qF -- "! not replayed, left in dlq-scoring-hold: $want" "$t/err" || fail "not named: $want
in: $(cat "$t/err")"
done
calls '([pubs[] | .argv[3]] == ["ryft-ingested", "ryft-session-completed", "ryft-ingested"])
  and (pubs[0] | attrs) == {"ryft-key": "ingested", "ryft-message-id": "m1", "ryft-reason": "sync", "ryft-replayed": "true", "trace": "kept"}
  and (pubs[1] | attrs) == {"ryft-key": "session.completed", "ryft-message-id": "m2", "ryft-replayed": "true"}
  and (pubs[2] | attrs) == {"ryft-key": "ingested", "ryft-message-id": "m3", "ryft-replayed": "true"}
  and [pubs[] | flag("ordering-key")] == [$u1, $u2, $u1]
  and [pubs[] | flag("message")] == [body("scoring"; 0), body("scoring"; 1), body("scoring"; 2)]
  and acks == ["L1", "L2", "L3"]
  and [.[] | select(cmd == "pubsub topics publish" or cmd == "pubsub subscriptions ack") | cmd]
      == ([range(3)] | map("pubsub topics publish", "pubsub subscriptions ack"))
  and ([.[] | select(cmd == "pubsub subscriptions modify-message-ack-deadline")][0] | flag("ack-deadline") == "600" and (ids | length) == 9)
  and ([.[] | select(cmd == "pubsub subscriptions modify-message-ack-deadline")][0].n < pubs[0].n)
  and (released | sort) == ["L4", "L5", "L6", "L7", "L8", "L9"]
  and all(.[]; has_flag("auto-ack") | not)' "the replay of scoring's letters"
calls 'letter_off_argv' "a replay's data, attributes and ordering key are in a private flags file, never on argv"
left dlq-scoring-hold L4 L5 L6 L7 L8 L9; clean "the replay"
# One letter: the first, and nothing else pulled.
reset
out=$(run --project "$P" replay scoring 1 2>"$t/err") && [ "$out" = "replayed 1, refused 0" ] || fail "replay scoring 1: $out $(cat "$t/err")"
calls '[.[] | select(cmd == "pubsub subscriptions pull") | flag("limit")] == ["1"] and acks == ["L1"]' "replay scoring 1"
left dlq-scoring-hold L2 L3 L4 L5 L6 L7 L8 L9

# 5. notify, with --force anywhere: N1 back to ryft-notify-wait under its key, N2 to ryft-refresh, no
# ordering key; N3 (a key notify does not handle) and N4 (no key) refused and left.
for args in "replay notify --force" "--force replay notify" "replay --force dlq-notify-hold"; do
  read -ra words <<< "$args"
  reset; rc=0
  out=$(run --project "$P" "${words[@]}" 2>"$t/err") || rc=$?
  [ "$rc" = 1 ] && [ "$out" = "replayed 2, refused 2" ] || fail "'$args': $rc $out $(cat "$t/err")"
  grep -qF 'message_id "n3" (letter N3), key "ingested", dead-lettered by Pub/Sub from notify-wait: its ryft-key "ingested" is not a key notify handles' "$t/err" \
    || fail "'$args': N3 not named: $(cat "$t/err")"
  grep -qF '(letter N4), key null, filed by the consumer: filed by notify without a ryft-key' "$t/err" || fail "'$args': N4 not named: $(cat "$t/err")"
  calls '[pubs[] | .argv[3]] == ["ryft-notify-wait", "ryft-refresh"]
    and (pubs[0] | attrs) == {"ryft-key": "scored", "ryft-message-id": "n1", "ryft-reason": "sync", "ryft-replayed": "true"}
    and (pubs[1] | attrs) == {"ryft-key": "refresh", "ryft-message-id": "n2", "ryft-replayed": "true"}
    and [pubs[] | has_flag("ordering-key")] == [false, false]
    and [pubs[] | flag("message")] == [body("notify"; 0), body("notify"; 1)]
    and acks == ["N1", "N2"] and (released | sort) == ["N3", "N4"] and letter_off_argv' "'$args'"
  left dlq-notify-hold N3 N4
done
reset; run --project "$P" replay notify --force 1 >/dev/null 2>&1 || fail "replay notify --force 1"
calls '[.[] | select(cmd == "pubsub subscriptions pull") | flag("limit")] == ["1"]' "--force taken as the count"

# 6. A publish that fails stops the run: nothing acked, every letter handed back. An ack that fails
# after its publish: the letter stays (a later replay sends it again), the run stops.
reset; rc=0
out=$(FAKE_FAIL="pubsub topics publish" run --project "$P" replay scoring 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && [ "$out" = "replayed 0, refused 0" ] || fail "a failed publish: $rc $out $(cat "$t/err")"
grep -q 'the publish to ryft-ingested failed, the letter stays in dlq-scoring-hold: message_id "m1"' "$t/err" || fail "a failed publish is not said: $(cat "$t/err")"
calls '(pubs | length) == 1 and acks == [] and (released | sort) == ["L1","L2","L3","L4","L5","L6","L7","L8","L9"]' "a failed publish"
left dlq-scoring-hold L1 L2 L3 L4 L5 L6 L7 L8 L9; clean "a failed publish"
reset; rc=0
out=$(FAKE_FAIL="pubsub subscriptions ack" run --project "$P" replay scoring 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && [ "$out" = "replayed 0, refused 0" ] || fail "a failed ack: $rc $out $(cat "$t/err")"
grep -q 'replayed to ryft-ingested (message pub-[0-9]*), but the ack failed: the letter stays in dlq-scoring-hold' "$t/err" || fail "a failed ack is not said: $(cat "$t/err")"
calls '(pubs | length) == 1 and (released | sort) == ["L1","L2","L3","L4","L5","L6","L7","L8","L9"]' "a failed ack"
left dlq-scoring-hold L1 L2 L3 L4 L5 L6 L7 L8 L9
# A pull that does not answer JSON is an error, not "empty"; an empty hold is.
reset; rc=0
out=$(FAKE_GARBAGE=1 run --project "$P" replay scoring 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && grep -q 'did not answer a list of messages' "$t/err" || fail "a garbage pull: $rc $out $(cat "$t/err")"
if grep -q empty <<<"$out"; then fail "a garbage pull is not empty: $out"; fi
reset; echo '[]' > "$t/store/dlq-scoring-hold.json"
out=$(run --project "$P" replay scoring 2>"$t/err") && [ "$out" = "dlq-scoring-hold is empty" ] || fail "empty: $out $(cat "$t/err")"

# 7. The emulator (compose's): its endpoint, no credentials.
reset
PUBSUB_EMULATOR_HOST=localhost:8085 run --project ryft-local list >/dev/null 2>"$t/err" || fail "list on the emulator: $(cat "$t/err")"
calls 'length > 0 and all(.[]; .endpoint == "http://localhost:8085/" and .nocred == "true" and .argv[-2] == "--project=ryft-local")' "the emulator's calls"

# 8. purge asks; anything but PURGE deletes nothing; PURGE seeks to the time it started.
reset; rc=0
out=$(printf 'yes\n' | run --project "$P" purge notify 2>"$t/err") || rc=$?
[ "$rc" = 1 ] && [ "$out" = "nothing deleted" ] || fail "purge without PURGE: $rc $out $(cat "$t/err")"
calls '[.[] | select(cmd == "pubsub subscriptions seek")] == [] and acks == [] and (released | sort) == ["N1","N2","N3","N4"]' "purge without PURGE"
left dlq-notify-hold N1 N2 N3 N4
reset
out=$(printf 'PURGE\n' | run --project "$P" purge notify 2>"$t/err") || fail "purge: $(cat "$t/err")"
grep -q '^dlq-notify-hold purged: every letter published before 20[0-9-]*T[0-9:]*Z$' <<<"$out" || fail "purge said: $out"
calls '[.[] | select(cmd == "pubsub subscriptions seek") | [.argv[3], (flag("time") | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))]]
  == [["dlq-notify-hold", true]] and released == []' "purge"
left dlq-notify-hold; clean purge

# Over every run: no --auto-ack, no kubectl, and every call named the project.
reset
jq -e -s 'length > 0 and all(.[]; (.argv | index("--auto-ack") | not) and any(.argv[]; startswith("--project=")) and .nolog == "true")' \
  "$t/all-calls.jsonl" >/dev/null || fail "a call acked on pull, named no project or had gcloud's file log on"
[ ! -e "$t/kubectl.calls" ] || fail "kubectl was called: $(cat "$t/kubectl.calls")"

echo "✓ dlq_test: arguments before any call, --project on every call, the regional endpoint (the emulator's without credentials), list and show without ack, a replay of only the handled keys under the key its source gives, marked, with its attributes, data and ordering key (in a private flags file, never on argv), acked after its publish, foreign keys refused and left, a failed publish or ack stops, notify needs --force, purge asks"

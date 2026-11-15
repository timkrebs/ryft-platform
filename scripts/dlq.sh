#!/usr/bin/env bash
# The dead letters on Pub/Sub (design 2.2, D218; docs/OPERATIONS.md → GCP → Dead letters and the outbox):
# the hold subscriptions dlq-scoring-hold and dlq-notify-hold keep for 14 days what ryft-dlq-scoring and
# ryft-dlq-notify receive. Through gcloud, as you, in the project you name (there is no default):
#
#   scripts/dlq.sh --project <id> list                     per hold subscription the letters pulled, one
#                                                          line each (nothing acked)
#   scripts/dlq.sh --project <id> show <consumer> [n|all]  the first n letters in full, the data decoded
#                                                          (default 10; nothing acked)
#   scripts/dlq.sh --project <id> replay <consumer> [n|all] [--force]
#                                                          each letter back to its topic as a redelivery
#                                                          (ryft-replayed=true, its own attributes), then
#                                                          acked
#   scripts/dlq.sh --project <id> purge <consumer>         every letter of the consumer's hold subscription
#                                                          published until now (asks first)
#
# <consumer> is scoring or notify (or dlq-scoring-hold / dlq-notify-hold); n is 1 to 1000, all is 1000.
# list and show pull without --auto-ack and hand the letters back at once; one pass returns at most 1000
# and may return fewer than a subscription holds (Monitoring's num_undelivered_messages is the count).
#
# A replay republishes only the keys the consumer handles (scoring: ingested, session.completed; notify:
# scored, session.scored, refresh), read from infra/gcp/pubsub.auto.tfvars.json, which cmd/pubsub-topology
# writes from topology.go. The key is read where a publisher cannot write it (D150):
#   - a letter Pub/Sub dead-lettered (attribute CloudPubSubDeadLetterSourceSubscription) is filed under its
#     source subscription's key (scoring-ingested: ingested); its ryft-key must be absent or that key; from
#     notify-wait the key is its ryft-key, one of notify's. It goes back to that subscription's topic.
#   - a letter the consumer filed itself (ryft-error, no source subscription) is filed under its ryft-key,
#     which the consumer set from the push path, and goes to that key's topic.
# Anything else is refused, named, and left in the hold subscription; the script then exits 1. notify's
# letters are not replayed without --force: a push hours late is wrong (D84); list and purge them.
# A replay keeps the letter's attributes but Pub/Sub's CloudPubSubDeadLetter* and ryft-error, sets
# ryft-key and ryft-replayed=true, and on an ordered topic the ordering key (the event's user_id). A letter
# is acked only after its publish succeeded: what is not replayed stays (handed back at the end, and Pub/Sub
# redelivers it after its lease anyway). A publish or an ack that fails stops the run.
#
# Every argument is checked before gcloud runs. The project is always named (--project on every call, never
# gcloud's configured one), and calls go to the regional endpoint europe-west1-pubsub.googleapis.com
# (ordering keys hold per region). With PUBSUB_EMULATOR_HOST set (compose's: localhost:8085, project
# ryft-local) they go to the emulator, without credentials. A letter's data is its event's JSON: the user id,
# the day or days, and where the event has them a session id, the reason and the request id (scored: user_id,
# day and request_id only); no scores or other health values. Its ryft-message-id names the user and day too
# (scored:<user_id>:<day>). So a replay hands gcloud the data, the attributes and the ordering key in a flags
# file (--flags-file, mode 600 in the script's private temporary directory, deleted after each call): `ps`
# shows the topic and the file's name, not the letter. gcloud's own file log, which records the arguments, is off.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
TOPOLOGY=$HERE/../infra/gcp/pubsub.auto.tfvars.json
MAX=1000
BATCH=100       # letters per pull for list, show and purge
REPLAY_BATCH=50 # letters per pull for a replay: each held 600 s, two gcloud calls each
LEASE=600       # seconds a replay holds the letters it is working on (Pub/Sub's maximum)

help() { awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; }
usage() { help >&2; exit 2; }
refuse() { echo "✗ dlq.sh: $*" >&2; exit 2; }

force='' project='' pos=()
while [ $# -gt 0 ]; do
  case "$1" in
    --force) force=1 ;;
    --project) [ $# -ge 2 ] && [ -n "$2" ] || refuse "--project needs a project id"; project=$2; shift ;;
    --project=?*) project=${1#--project=} ;;
    -h|--help) help; exit 0 ;;
    -*) echo "✗ dlq.sh: unknown option $1" >&2; usage ;;
    *) pos+=("$1") ;;
  esac
  shift
done
[ "${#pos[@]}" -gt 0 ] || usage
cmd=${pos[0]}
consumer_of() {
  case "$1" in
    scoring|dlq-scoring-hold) echo scoring ;;
    notify|dlq-notify-hold) echo notify ;;
    *) refuse "the consumer is scoring or notify (dlq-scoring-hold, dlq-notify-hold), not $1" ;;
  esac
}
count_of() { # n|all → 1..MAX
  if [ "$1" = all ]; then echo $MAX; return; fi
  [[ "$1" =~ ^[1-9][0-9]{0,3}$ ]] && [ "$1" -le $MAX ] || refuse "n is 1 to $MAX or all, not $1"
  echo "$1"
}
consumer='' n=''
case "$cmd" in
  list)
    [ "${#pos[@]}" = 1 ] || usage ;;
  show|replay)
    [ "${#pos[@]}" -ge 2 ] && [ "${#pos[@]}" -le 3 ] || usage
    consumer=$(consumer_of "${pos[1]}")
    if [ "${#pos[@]}" = 3 ]; then n=$(count_of "${pos[2]}"); elif [ "$cmd" = show ]; then n=10; else n=$MAX; fi ;;
  purge)
    [ "${#pos[@]}" = 2 ] || usage
    consumer=$(consumer_of "${pos[1]}") ;;
  *) usage ;;
esac
if [ -n "$force" ] && [ "$cmd" != replay ]; then refuse "--force is for a replay of notify's letters"; fi
if [ "$cmd" = replay ] && [ "$consumer" = notify ] && [ -z "$force" ]; then
  refuse "notify's dead letters are not replayed (a push hours late is wrong); purge them, or --force"
fi
[ -n "$project" ] || refuse "name the project: --project <id> (there is no default; production is ryftlab-prod)"
[[ "$project" =~ ^[a-z][a-z0-9-]{4,28}[a-z0-9]$ ]] || refuse "--project $project is not a project id"
if [ -n "${PUBSUB_EMULATOR_HOST:-}" ]; then
  [[ "$PUBSUB_EMULATOR_HOST" =~ ^[A-Za-z0-9.-]+:[0-9]{1,5}$ ]] || refuse "PUBSUB_EMULATOR_HOST $PUBSUB_EMULATOR_HOST is not host:port"
  endpoint="http://$PUBSUB_EMULATOR_HOST/"
  export CLOUDSDK_AUTH_DISABLE_CREDENTIALS=true
else
  endpoint=https://europe-west1-pubsub.googleapis.com/
  unset CLOUDSDK_AUTH_DISABLE_CREDENTIALS
fi
export CLOUDSDK_API_ENDPOINT_OVERRIDES_PUBSUB=$endpoint CLOUDSDK_CORE_DISABLE_PROMPTS=1 CLOUDSDK_CORE_DISABLE_FILE_LOGGING=true
command -v gcloud >/dev/null 2>&1 || refuse "gcloud is not installed"
command -v jq >/dev/null 2>&1 || refuse "jq is not installed"
[ -r "$TOPOLOGY" ] || refuse "no $TOPOLOGY (cmd/pubsub-topology writes it)"

# The topology and the verdict on one letter (a received message: ackId, message), in jq. $topo[0] is
# pubsub.auto.tfvars.json, $c the consumer, $project the project.
JQ_DEFS='
def push: $topo[0].pubsub_push_subscriptions;
def mine: push | to_entries | map(select(.value.service == $c and .value.dead_letter_topic != ""));
def handled: [mine[] | .value.path | select(startswith("/push/") and . != "/push/wait") | ltrimstr("/push/")];
def handles($k): ($k | type) == "string" and any(handled[]; . == $k);
def topic_of($k): [mine[] | select(.value.path == "/push/" + $k) | .value.topic][0];
def ordered($t): any(push[]; .topic == $t and .ordered);
def hold: ([mine[] | .value.dead_letter_topic] | unique) as $t
  | if ($t | length) != 1 then empty
    else [$topo[0].pubsub_hold_subscriptions | to_entries[] | select(.value.topic == $t[0]) | .key]
      | if length == 1 then .[0] else empty end end;
def standard: gsub("-"; "+") | gsub("_"; "/") | . as $s | $s + ("===" | .[0:((4 - ($s | length) % 4) % 4)]);
def decide:
  (.message // {}) as $m
  | ($m.attributes // {}) as $a
  | $a["ryft-key"] as $rk
  | (($a.CloudPubSubDeadLetterSourceSubscription // "") | sub("^projects/[^/]+/subscriptions/"; "")) as $src
  | (($m.data // "") | standard) as $b64
  | (try ($b64 | @base64d) catch null) as $raw
  # The data passes as a JSON string in the flags file, which gcloud reads as text: refused unless it decodes
  # to text and back, holds no NUL and does not end in a line break (no event does; such a letter is checked
  # by hand).
  | (if $raw != null and ($raw | @base64) == $b64 and ($raw | explode | any(. == 0) | not) and ($raw | endswith("\n") | not)
     then $raw else null end) as $text
  | (if $src != "" then "pubsub" elif $a | has("ryft-error") then "consumer" else "unknown" end) as $by
  | (if $by == "pubsub" then
       ([mine[] | select(.key == $src) | .value][0]) as $sub
       | if $sub == null then {why: "dead-lettered from \($src | tojson), which is not one of \($c)'"'"'s subscriptions"}
         elif ($a | has("CloudPubSubDeadLetterSourceSubscriptionProject")) and $a.CloudPubSubDeadLetterSourceSubscriptionProject != $project
           then {why: "dead-lettered in project \($a.CloudPubSubDeadLetterSourceSubscriptionProject | tojson), not \($project)"}
         elif $sub.path == "/push/wait" then
           if $rk == null then {why: "from \($src) without a ryft-key"}
           elif handles($rk) | not then {why: "its ryft-key \($rk | tojson) is not a key \($c) handles"}
           else {key: $rk, topic: $sub.topic} end
         else ($sub.path | ltrimstr("/push/")) as $k
           | if handles($k) | not then {why: "its subscription'"'"'s key \($k | tojson) is not one \($c) handles"}
             elif $rk != null and $rk != $k then {why: "its ryft-key \($rk | tojson) is not the key of \($src) (\($k))"}
             else {key: $k, topic: $sub.topic} end
         end
     elif $by == "consumer" then
       if $rk == null then {why: "filed by \($c) without a ryft-key"}
       elif handles($rk) | not then {why: "its ryft-key \($rk | tojson) is not a key \($c) handles"}
       else {key: $rk, topic: topic_of($rk)} end
     else {why: "neither dead-lettered by Pub/Sub (no CloudPubSubDeadLetterSourceSubscription) nor filed by \($c) (no ryft-error): check by hand"}
     end) as $d
  | (if $d.why then $d
     elif $text == null then {why: "its data is not text gcloud can pass on: check by hand"}
     elif $text == "" then {why: "it has no data"}
     elif ordered($d.topic) then
       (try ($text | fromjson | .user_id) catch null) as $u
       | if ($u | type) == "string" and ($u | test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))
         then $d + {ordering_key: $u}
         else {why: "\($d.topic) is ordered by the event'"'"'s user_id, and its data has none"} end
     else $d end) as $d
  | (if $d.why then {}
     else ($a | with_entries(select((.key | startswith("CloudPubSubDeadLetter") | not) and .key != "ryft-error")))
       + {"ryft-key": $d.key, "ryft-replayed": "true"} end) as $pub
  # gcloud reads --attribute as k=v pairs split at a delimiter: one that no key or value holds.
  | ([("|", ";", "~", "#", "!", "%", "+", "@") as $x | select(all($pub | to_entries[]; (.key + .value) | contains($x) | not)) | $x][0]) as $delim
  | (if $d.why or $delim != null then $d else {why: "its attributes hold every delimiter gcloud takes: check by hand"} end) as $d
  | {
      letter_id: ($m.messageId // ""), message_id: ($a["ryft-message-id"] // ""), ack_id: (.ackId // ""),
      key: ($d.key // $rk), by: $by, source: (if $src == "" then null else $src end),
      attempts: ($a.CloudPubSubDeadLetterSourceDeliveryCount // null),
      error: (if $by == "consumer" then $a["ryft-error"] else null end),
      replayed_before: ($a["ryft-replayed"] == "true"), published: ($m.publishTime // null),
      verdict: (if $d.why then "refused" else "replay" end), why: ($d.why // null),
      topic: (if $d.why then null else $d.topic end), ordering_key: (if $d.why then null else $d.ordering_key end),
      attribute: (if $d.why then null else "^" + $delim + "^" + ($pub | to_entries | sort_by(.key) | map(.key + "=" + .value) | join($delim)) end),
      data: (if $d.why then null else $text end),
      attributes: $a, text: $text, base64: (if $text == null then $b64 else null end)
    };
def what: "message_id \(.message_id | tojson) (letter \(.letter_id)), key \(.key | tojson), "
  + (if .source then "dead-lettered by Pub/Sub from \(.source)" elif .by == "consumer" then "filed by the consumer" else "of unknown origin" end);
'
tq() { # CONSUMER PROGRAM [jq options and files...]: jq with the topology and the definitions above
  local c=$1 prog=$2; shift 2
  jq --slurpfile topo "$TOPOLOGY" --arg c "$c" --arg project "$project" "$JQ_DEFS $prog" "$@"
}
hold_of() { # CONSUMER → its hold subscription, from the topology
  local h
  h=$(tq "$1" 'hold' -rn) && [ -n "$h" ] || refuse "$TOPOLOGY names no single hold subscription for $1's dead-letter topic"
  echo "$h"
}
hold=''
[ -z "$consumer" ] || hold=$(hold_of "$consumer")
echo "→ project $project, $endpoint" >&2

# Letters pulled and not acked are handed back on the way out (ack deadline 0), with their newest ack id.
umask 077
tmp=$(mktemp -d "${TMPDIR:-/tmp}/dlq.XXXXXX")
: > "$tmp/leased.tsv"; : > "$tmp/acked.txt"; : > "$tmp/seen.txt"
gc() { gcloud "$@" "--project=$project" --quiet; }
release() { # HOLD ACK-ID... (at most 100): ack deadline 0, so they can be pulled again at once
  local h=$1 ids; shift
  ids=$(IFS=,; echo "$*")
  gc pubsub subscriptions modify-message-ack-deadline "$h" "--ack-ids=$ids" --ack-deadline=0 >/dev/null 2>&1 \
    || echo "! dlq.sh: $# letters of $h were not handed back: they return when their lease ends (at most $LEASE s)" >&2
}
cleanup() {
  local status=$? h batch=() id
  trap '' INT TERM HUP
  set +e
  awk -F'\t' -v a="$tmp/acked.txt" 'BEGIN { while ((getline l < a) > 0) acked[l] = 1 }
    !($2 in acked) { last[$1 "\t" $2] = $3 } END { for (k in last) { split(k, p, "\t"); print p[1] "\t" last[k] } }' \
    "$tmp/leased.tsv" | LC_ALL=C sort > "$tmp/release.tsv"
  for h in $(cut -f1 "$tmp/release.tsv" | sort -u); do
    batch=()
    while IFS= read -r id; do
      batch+=("$id")
      if [ "${#batch[@]}" -ge 100 ]; then release "$h" "${batch[@]}"; batch=(); fi
    done < <(awk -F'\t' -v h="$h" '$1 == h { print $2 }' "$tmp/release.tsv")
    [ "${#batch[@]}" = 0 ] || release "$h" "${batch[@]}"
  done
  rm -rf "$tmp"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

pull() { # HOLD LIMIT OUT: one pull, no ack; every lease recorded for the way out
  if ! gc pubsub subscriptions pull "$1" "--limit=$2" --format=json > "$3" 2>"$tmp/err"; then
    echo "✗ dlq.sh: the pull from $1 failed: $(head -c 300 "$tmp/err")" >&2; return 1
  fi
  [ -s "$3" ] || echo '[]' > "$3"
  if ! jq -e 'type == "array" and all(.[]; type == "object" and (.ackId | type) == "string" and (.message.messageId | type) == "string")' "$3" >/dev/null 2>&1; then
    echo "✗ dlq.sh: the pull from $1 did not answer a list of messages" >&2; return 1
  fi
  jq -r --arg h "$1" '.[] | [$h, .message.messageId, .ackId] | @tsv' "$3" >> "$tmp/leased.tsv"
}
pull_all() { # HOLD MAX OUT: up to MAX letters, each once (while one is leased a pull does not return it)
  local got=0 lim total
  echo '[]' > "$3"
  while [ "$got" -lt "$2" ]; do
    lim=$(( $2 - got < BATCH ? $2 - got : BATCH ))
    pull "$1" "$lim" "$tmp/batch.json" || return 1
    jq -s '(.[0] | map(.message.messageId)) as $have | .[0] + [.[1][] | select(.message.messageId as $id | any($have[]; . == $id) | not)]' \
      "$3" "$tmp/batch.json" > "$tmp/all.json"
    total=$(jq length "$tmp/all.json")
    mv "$tmp/all.json" "$3"
    [ "$total" -gt "$got" ] || break
    got=$total
  done
}
extend() { # HOLD FILE: the leases of FILE's letters to LEASE seconds
  local ids
  ids=$(jq -r '[.[].ackId] | join(",")' "$2")
  [ -n "$ids" ] || return 0
  gc pubsub subscriptions modify-message-ack-deadline "$1" "--ack-ids=$ids" "--ack-deadline=$LEASE" >/dev/null 2>"$tmp/err" \
    || { echo "✗ dlq.sh: the leases in $1 could not be extended: $(head -c 300 "$tmp/err")" >&2; return 1; }
}

case "$cmd" in
  list)
    for c in scoring notify; do
      h=$(hold_of "$c")
      pull_all "$h" $MAX "$tmp/$c.json"
      echo "$h: $(jq length "$tmp/$c.json")"
      tq "$c" '.[] | decide | {letter_id, message_id, key, by, source, attempts, error, replayed_before, published, verdict, why}' -c "$tmp/$c.json"
    done ;;
  show)
    pull_all "$hold" "$n" "$tmp/show.json"
    tq "$consumer" '[.[] | decide | {letter_id, message_id, published, key, by, source, attempts, error, replayed_before, verdict, why,
      attributes, data: (if .text == null then {base64} else (try (.text | fromjson) catch .text) end)}]' "$tmp/show.json" ;;
  purge)
    now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    pull_all "$hold" $MAX "$tmp/purge.json"
    count=$(jq length "$tmp/purge.json")
    answer=''
    read -r -p "delete every letter in $hold published before $now ($count pulled now)? type PURGE: " answer || true
    [ "$answer" = PURGE ] || { echo "nothing deleted"; exit 1; }
    gc pubsub subscriptions seek "$hold" "--time=$now" >/dev/null 2>"$tmp/err" \
      || { echo "✗ dlq.sh: the seek of $hold failed, nothing deleted: $(head -c 300 "$tmp/err")" >&2; exit 1; }
    jq -r '.[].message.messageId' "$tmp/purge.json" >> "$tmp/acked.txt"
    echo "$hold purged: every letter published before $now" ;;
  replay)
    ok=0 refused=0 handled=0 stopped='' rounds=0
    while [ "$handled" -lt "$n" ] && [ -z "$stopped" ] && [ "$rounds" -lt 200 ]; do
      rounds=$((rounds + 1))
      lim=$(( n - handled < REPLAY_BATCH ? n - handled : REPLAY_BATCH ))
      pull "$hold" "$lim" "$tmp/batch.json" || { stopped=1; break; }
      [ "$(jq length "$tmp/batch.json")" -gt 0 ] || break
      # The whole batch for LEASE seconds: refused letters stay out of the next pulls until the end.
      extend "$hold" "$tmp/batch.json" || { stopped=1; break; }
      tq "$consumer" '.[] | select(.message.messageId as $id | any($seen | split("\n")[]; . == $id) | not) | decide' \
        -c --rawfile seen "$tmp/seen.txt" "$tmp/batch.json" > "$tmp/new.jsonl"
      while IFS= read -r dec <&3; do
        jq -r .letter_id <<<"$dec" >> "$tmp/seen.txt"
        handled=$((handled + 1))
        what=$(tq "$consumer" 'what' -r <<<"$dec")
        if [ "$(jq -r .verdict <<<"$dec")" != replay ]; then
          echo "! not replayed, left in $hold: $what: $(jq -r .why <<<"$dec")" >&2
          refused=$((refused + 1))
          continue
        fi
        topic=$(jq -r .topic <<<"$dec")
        # The letter (data, attributes, ordering key) goes in a flags file, never on gcloud's command line.
        jq '{"--message": .data, "--attribute": .attribute} + (if .ordering_key then {"--ordering-key": .ordering_key} else {} end)' \
          <<<"$dec" > "$tmp/publish.json"
        published=1
        out=$(gc pubsub topics publish "$topic" "--flags-file=$tmp/publish.json" --format=json 2>"$tmp/err") || published=''
        rm -f "$tmp/publish.json"
        if [ -z "$published" ] || ! id=$(jq -er '.messageIds[0] | strings' <<<"$out" 2>/dev/null); then
          echo "✗ dlq.sh: the publish to $topic failed, the letter stays in $hold: $what: $(head -c 300 "$tmp/err")" >&2
          stopped=1; break
        fi
        if ! gc pubsub subscriptions ack "$hold" "--ack-ids=$(jq -r .ack_id <<<"$dec")" >/dev/null 2>"$tmp/err"; then
          echo "✗ dlq.sh: replayed to $topic (message $id), but the ack failed: the letter stays in $hold and a later replay sends it again (handlers are idempotent, D100): $what" >&2
          stopped=1; break
        fi
        jq -r .letter_id <<<"$dec" >> "$tmp/acked.txt"
        echo "replayed: $what → $topic (message $id)" >&2
        ok=$((ok + 1))
      done 3< "$tmp/new.jsonl"
    done
    if [ "$handled" = 0 ] && [ -z "$stopped" ]; then echo "$hold is empty"; exit 0; fi
    echo "replayed $ok, refused $refused"
    [ -z "$stopped" ] && [ "$refused" = 0 ] ;;
esac

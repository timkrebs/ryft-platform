#!/usr/bin/env bash
# Recovers a generated value that the rebuild imports into infra/vault before its first apply
# (D211), from the HCP Terraform workspace's state versions (the only copy left after the
# destroy of 2026-11-10):
#   default  random_bytes.data_key["api"], .attributes.base64 -> ~/.config/ryft/data-key-api
#            (the api's DATA_ENCRYPTION_KEY: it seals the stored Sign in with Apple tokens)
#   --demo   random_password.demo, .attributes.result         -> ~/.config/ryft/demo-password
#            (the App Review demo account)
# It takes the newest finalized state version that holds the resource (or the one named), and
# prints only that version's id, created-at and serial, a short SHA-256 fingerprint of the value
# exactly as written (no newline), and for the key its decoded length (must be 32). The value is
# never printed and never sits in a shell variable: jq writes it to a file, which is 0600 and
# never overwritten.
#
#   scripts/recover_data_key.sh [--demo] [--dry-run] [--org O] [--workspace W] [sv-<id>]
#     sv-<id>    this state version instead of the newest one that holds the resource
#     --dry-run  find and check the value, write nothing (compare two versions' fingerprints)
#     --org, --workspace  default tim-krebs-org, ryft-prod-euc1-vault (infra/vault/terraform.tf)
#
# Token: TF_TOKEN_app_terraform_io, else credentials["app.terraform.io"].token in
# ~/.terraform.d/credentials.tfrc.json (terraform login). The token and every URL reach curl as a
# config on stdin (header = ..., url = ...), never in argv, where ps shows them to every user of
# the machine: a version's signed hosted-state-download-url alone opens the whole raw state. The
# token goes only to https://app.terraform.io itself (curl drops it on a redirect to another host;
# -q keeps ~/.curlrc out, where a --verbose would print it and --location-trusted would forward
# it). HCP Terraform API (v2): GET organizations/:org/workspaces/:name (404: deleted, or not
# visible to the token), GET state-versions?filter[workspace][name]=&filter[organization][name]=
# (paged), GET state-versions/:id, then the version's hosted-state-download-url (the raw state,
# which holds every credential of infra/vault: kept in a 0700 temp dir, removed on exit). There
# is no environment switch for the API host; the tests put a fake curl on PATH.
#
# Time Machine: before the write, tmutil addexclusion ~/.config/ryft (whenever tmutil is on PATH,
# so on macOS; the sticky form, an attribute on the folder, no sudo) keeps the folder out of every
# backup from then on. If that fails the script warns and writes anyway: exclude the folder by hand
# (System Settings > General > Time Machine > Options) before the next backup. A local snapshot is
# of the whole volume, exclusions or not: one taken while the file exists keeps it past the rm
# until it ages out (about 24 hours; tmutil listlocalsnapshots / lists them).
#
# Exit: 0 found (and written); 1 not recoverable or refused; 2 usage, missing tool or no token.
set -euo pipefail

die() { local rc=$1; shift; echo "✗ recover_data_key: $*" >&2; exit "$rc"; }
note() { echo "• $*" >&2; }
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p;}' "$0"; }

org=tim-krebs-org ws=ryft-prod-euc1-vault demo=0 dry=0 version=
while [ $# -gt 0 ]; do
  case "$1" in
    --demo) demo=1 ;;
    --dry-run) dry=1 ;;
    --org|--workspace)
      [ $# -ge 2 ] || die 2 "$1 needs a name"
      if [ "$1" = --org ]; then org=$2; else ws=$2; fi
      shift ;;
    -h|--help) usage; exit 0 ;;
    sv-*) [ -z "$version" ] || die 2 "one state version at most"; version=$1 ;;
    *) die 2 "unknown argument '$1' (--help)" ;;
  esac
  shift
done
for n in "$org" "$ws"; do
  [[ "$n" =~ ^[A-Za-z0-9_-]+$ ]] || die 2 "'$n' is not an organization or workspace name"
done
[ -z "$version" ] || [[ "$version" =~ ^sv-[A-Za-z0-9]+$ ]] || die 2 "'$version' is not a state version id (sv-...)"

if [ "$demo" = 1 ]; then
  type=random_password name=demo key='' attr=result file=demo-password label='random_password.demo'
  fallback="without it: scripts/seed_demo.sh with the newly generated password, then App Store Connect (docs/OPERATIONS.md -> App Review demo account)"
else
  type=random_bytes name=data_key key=api attr=base64 file=data-key-api label='random_bytes.data_key["api"]'
  fallback="without it: the one-off statement that clears the unreadable Apple tokens (D211, docs/REBUILD.md), which cannot be undone"
fi
dest_dir=$HOME/.config/ryft
dest=$dest_dir/$file
# Refuse before any request (a dangling symlink too); the write below refuses again, atomically.
[ "$dry" = 1 ] || { [ ! -e "$dest" ] && [ ! -L "$dest" ]; } || die 1 "$dest exists: refusing to overwrite it (move it away first, or --dry-run)"

for t in curl jq base64; do command -v "$t" >/dev/null || die 2 "$t is required"; done
if command -v sha256sum >/dev/null; then sha256() { sha256sum; }
elif command -v shasum >/dev/null; then sha256() { shasum -a 256; }
else die 2 "sha256sum or shasum is required"; fi

token=${TF_TOKEN_app_terraform_io:-}
creds=$HOME/.terraform.d/credentials.tfrc.json
if [ -z "$token" ] && [ -f "$creds" ]; then
  token=$(jq -r '.credentials["app.terraform.io"].token // empty' "$creds" 2>/dev/null) || token=
fi
[ -n "$token" ] || die 2 "no HCP Terraform token: set TF_TOKEN_app_terraform_io, or run terraform login (it writes $creds)"
# It goes into a curl config line between double quotes: nothing that could end or escape it.
[[ "$token" =~ ^[A-Za-z0-9._-]+$ ]] || die 2 "the HCP Terraform token has characters a token does not have; check TF_TOKEN_app_terraform_io or $creds"

umask 077
work=$(mktemp -d)
# The raw state holds every credential of infra/vault: gone on any way out, a Ctrl-C included.
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

api=https://app.terraform.io
# A URL goes into a curl config line between double quotes as well: https, and nothing that could
# end or escape the quoted value or the line (a quote, a backslash, white space, a control character).
url_re='^https://[^"\\[:space:][:cntrl:]]+$'
# fetch <url> <out>: the body into <out>, the HTTP status on stdout (000: no answer). The URL (a
# signed download URL is a credential) and, only for the API's own origin, the bearer reach curl
# through a config on stdin (printf is a builtin: neither is ever in an argv).
fetch() {
  local cfg='' code
  # Every caller passes a checked URL; this guards the config line itself.
  [[ "$1" =~ $url_re ]] || die 1 "a request URL that is not https or holds a quote, a backslash or white space: not passed to curl"
  case "$1" in
    "$api/api/v2/"*) cfg="header = \"Authorization: Bearer $token\""$'\n''header = "Accept: application/vnd.api+json"'$'\n' ;;
    "$api/"*) cfg="header = \"Authorization: Bearer $token\""$'\n' ;;
  esac
  cfg+="url = \"$1\""
  code=$(printf '%s\n' "$cfg" | curl -q --config - --silent --show-error --location --max-redirs 3 \
    --proto =https --proto-redir =https --connect-timeout 10 --max-time 120 \
    --output "$2" --write-out '%{http_code}') || true
  printf '%s' "${code:-000}"
}
# api_ok <code> <what>: dies on anything but 200, with the reason in words.
api_ok() {
  case "$1" in
    200) ;;
    401) die 1 "HCP Terraform refused the token (401): terraform login, or a fresh TF_TOKEN_app_terraform_io" ;;
    403) die 1 "$2: forbidden (403: the token may not read this workspace's state versions)" ;;
    # HCP Terraform answers 404, not 403, for what the token may not see: the two look the same.
    404) die 1 "$2: not found (404: deleted, or this token cannot see it; the API answers both alike). Before you treat $label as lost, look in the HCP Terraform UI (https://app.terraform.io/app/$org/workspaces/$ws) as a user with access to $org; if it is there, run this again with a token that can read it. $fallback" ;;
    000) die 1 "$2: no answer from app.terraform.io (network, proxy?)" ;;
    *) die 1 "$2: HTTP $1" ;;
  esac
}

code=$(fetch "$api/api/v2/organizations/$org/workspaces/$ws" "$work/workspace.json")
api_ok "$code" "workspace $ws in $org"
ws_id=$(jq -r '.data.id // empty' "$work/workspace.json")
[ -n "$ws_id" ] || die 1 "workspace $ws: the answer has no id"

# Candidates, newest first: id, created-at, serial, download URL (unit-separated: no field collapses).
row='[.id, (.attributes["created-at"] // ""), (.attributes.serial // "" | tostring), (.attributes["hosted-state-download-url"] // "")] | join("\u001f")'
if [ -n "$version" ]; then
  code=$(fetch "$api/api/v2/state-versions/$version" "$work/version.json")
  api_ok "$code" "state version $version"
  owner=$(jq -r '.data.relationships.workspace.data.id // empty' "$work/version.json")
  if [ -z "$owner" ]; then note "$version: the answer does not name its workspace; taken as $ws's"
  elif [ "$owner" != "$ws_id" ]; then die 1 "$version belongs to workspace $owner, not to $ws ($ws_id)"; fi
  jq -r ".data | $row" "$work/version.json" > "$work/candidates"
else
  page=1
  while :; do
    q="filter%5Bworkspace%5D%5Bname%5D=$ws&filter%5Borganization%5D%5Bname%5D=$org&page%5Bsize%5D=100&page%5Bnumber%5D=$page"
    code=$(fetch "$api/api/v2/state-versions?$q" "$work/page-$page.json")
    api_ok "$code" "the state versions of $ws"
    next=$(jq -r '.meta.pagination["next-page"] // empty' "$work/page-$page.json")
    [ -n "$next" ] || break
    [[ "$next" =~ ^[0-9]+$ ]] && [ "$next" -gt "$page" ] || die 1 "the state version list: page $page names next page '$next'"
    [ "$next" -le 100 ] || die 1 "more than 100 pages of state versions: name the version (sv-...)"
    page=$next
  done
  all=$(jq -s '[.[].data[]?] | length' "$work"/page-*.json)
  # Pending or discarded versions, and those whose data was deleted, have no state to read.
  jq -rs "[.[].data[]?] | map(select((.attributes.status // \"finalized\") == \"finalized\"))
    | sort_by([(.attributes[\"created-at\"] // \"\"), (.attributes.serial // 0)]) | reverse | .[] | $row" \
    "$work"/page-*.json > "$work/candidates"
  usable=$(wc -l < "$work/candidates" | tr -d ' ')
  [ "$usable" -gt 0 ] || die 1 "$ws has no finalized state version ($all in all). $fallback"
  [ "$usable" = "$all" ] || note "$((all - usable)) of $all state versions are not finalized (no state to read): skipped"
fi

# The resource in the root module, the instance with that index key (none for demo), not deposed;
# raw output without a newline, straight into the file.
pick='if (.version // null) != 4 then error("state format \(.version // "missing"), want 4") else . end
  | [ (.resources // [])[]
      | select(.mode == "managed" and .type == $type and .name == $name and ((.module // "") == ""))
      | (.instances // [])[]
      | select(has("deposed") | not)
      | select(if $key == "" then (has("index_key") | not) else (.index_key == $key) end)
      | (.attributes // {})[$attr]
      | select(type == "string" and length > 0) ]
  | if length > 1 then error("\(length) instances match") else (.[0] // empty) end'

found='' skipped=0
while IFS=$'\x1f' read -r -u 3 id created serial url; do
  [ -n "$url" ] || die 1 "$id ($created) has no download URL (its data deleted?): name an older version (sv-...) if you know one held the value"
  [[ "$url" =~ $url_re ]] || die 1 "$id: the download URL is not https, or holds a quote, a backslash or white space: not passed to curl"
  code=$(fetch "$url" "$work/state.json")
  [ "$code" = 200 ] || die 1 "the state of $id: HTTP $code (the URL expires after a while: run again)"
  : > "$work/value"
  if ! jq -j --arg type "$type" --arg name "$name" --arg key "$key" --arg attr "$attr" "$pick" \
       "$work/state.json" > "$work/value" 2> "$work/jq.err"; then
    die 1 "the state of $id is not a Terraform state this script reads: $(head -c 200 "$work/jq.err")"
  fi
  rm -f "$work/state.json"
  if [ -s "$work/value" ]; then found=$id; break; fi
  [ -z "$version" ] || die 1 "$version ($created, serial $serial) does not hold $label. $fallback"
  note "$id ($created, serial $serial): no $label"
  skipped=$((skipped + 1))
done 3< "$work/candidates"
[ -n "$found" ] || die 1 "none of the $skipped state versions of $ws holds $label. $fallback"

if [ "$demo" = 1 ]; then
  # operator.tf: length 24, special false. A value of another shape is not what the config
  # generates; an import of it would plan a replacement.
  if ! LC_ALL=C grep -Eq '^[A-Za-z0-9]{24}$' "$work/value"; then
    note "the value is not 24 alphanumeric characters (what operator.tf generates): an import of it plans a replacement"
  fi
else
  # sealed.ParseKey: base64 of exactly 32 bytes, or the api does not start.
  chars=$(wc -c < "$work/value" | tr -d ' ')
  bytes=0
  if LC_ALL=C grep -Eq '^[A-Za-z0-9+/]+={0,2}$' "$work/value"; then
    bytes=$(base64 --decode < "$work/value" 2>/dev/null | wc -c | tr -d ' ') || bytes=0
  fi
  [ "$chars" = 44 ] && [ "$bytes" = 32 ] || die 1 "$found holds a $label that is not base64 of 32 bytes ($chars characters, $bytes bytes decoded): not written"
fi
fp=$(sha256 < "$work/value" | cut -c1-16)

echo "state version: $found"
echo "created-at:    $created"
echo "serial:        $serial"
echo "sha256:        $fp (first 16 hex digits, of the value as written, no newline)"
[ "$demo" = 1 ] || echo "decoded:       $bytes bytes"
[ "$skipped" = 0 ] || note "$skipped newer state version(s) did not hold it (the destroy)"
if [ "$dry" = 1 ]; then
  echo "written:       nothing (--dry-run)"
  exit 0
fi
mkdir -p "$dest_dir"
# Time Machine, before the file exists: the sticky exclusion (an attribute on the folder, it moves
# with it; -p, the fixed-path form, needs root). A failure is a warning, not a stop: the key matters
# more, and the folder can be excluded by hand.
if command -v tmutil >/dev/null; then
  if tmutil addexclusion "$dest_dir" >/dev/null 2>"$work/tmutil.err"; then
    note "$dest_dir: excluded from Time Machine backups (a local snapshot of the volume still holds what it caught: --help)"
  else
    why=$(head -c 200 "$work/tmutil.err" | tr '\n' ' '); why=${why% }
    note "warning: tmutil addexclusion $dest_dir failed${why:+ ($why)}: exclude the folder by hand (System Settings > General > Time Machine > Options) before the next backup"
  fi
fi
# noclobber opens with O_EXCL: an existing file (or one created since the check above) stays.
if ! (set -o noclobber; cat "$work/value" > "$dest") 2>/dev/null; then
  die 1 "$dest exists: refusing to overwrite it"
fi
chmod 600 "$dest"
echo "written:       $dest (0600)"

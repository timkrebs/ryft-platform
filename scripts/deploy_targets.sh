#!/usr/bin/env bash
# Which image groups does a push to main have to build and pin? (D107; docs/OPERATIONS.md
# → Deploy and roll back.) Used by CI's `changes` job and by promote.sh, so both read the
# same tree and agree. Prints key=value lines, ready for $GITHUB_OUTPUT:
#   services=false  services_pin=0fd4256  services_why=…
#   web=true        web_pin=f437747       web_why=pin not in this history
# Groups: services (ingest, scoring, api, notify, migrate: from backend/) and web (web:
# from web/). The pins are read from deploy/k8s/overlays/eks/kustomization.yaml at HEAD.
# A group is targeted when, in this order:
#   1. it is held (`# hold: <group> <YYYY-MM-DD> <reason>` in the overlay) → NOT targeted, a warning
#   2. FORCE names it (services | web | all)                    → targeted
#   3. its pins disagree (services only) or are missing         → targeted
#   4. its pin is no commit of this history (a ryft-web SHA, a typo) → targeted
#   5. its directory differs between the pin and HEAD           → targeted, else not
# The comparison is with the RUNNING pin, not with the previous commit: a change whose run
# was cancelled or whose deploy PR was superseded stays "not deployed yet". The merged
# pin commit changes neither directory, so it targets nothing.
# Holds fail closed: a "#" followed by "hold" (any case) ANYWHERE on a line (also after a
# newTag) that is not a standalone line reading `# hold: <services|web> <YYYY-MM-DD> <reason>`
# stops the script (exit 2), in every mode, so a typo cannot silently stop being a hold.
#   FORCE  none (default) | services | web | all  (CI's workflow_dispatch input)
#
#   scripts/deploy_targets.sh --check [<base-ref>]
# CI's `manifests` job on every run: validates the holds, writes the active ones to
# $GITHUB_STEP_SUMMARY, and, given the pull request's base (origin/main), annotates a hold
# that the PR adds or removes and FAILS a PR from a deploy/<sha> branch (HEAD_REF, the App's
# own branches) that changes the newTag of a group held on the base. (An older open deploy PR
# therefore cannot undo a rollback; people's PRs, a second rollback among them, are not
# blocked; a rollback PR that moves newTag and adds the hold in one commit passes, because
# the base is not held yet; a PR that only removes the hold passes.) It also requires every
# RYFT image entry in the overlay to be exactly `- name: ghcr.io/timkrebs/ryft/<name>` and
# `newTag: "<7 hex>"`: a newName or digest line would silently change what runs.
#   HEAD_REF  the pull request's head branch (github.head_ref)
# Needs the full history (fetch-depth: 0). bash 3.2-safe (macOS).
set -euo pipefail
cd "$(dirname "$0")/.."
overlay=deploy/k8s/overlays/eks/kustomization.yaml
mode=targets; base=
if [ "${1:-}" = --check ]; then mode=check; base=${2:-}; fi
force=${FORCE:-none}
case "$force" in none|services|web|all|"") ;; *) echo "deploy_targets: FORCE must be none, services, web or all, not '$force'" >&2; exit 2 ;; esac

group_dir()    { case "$1" in services) echo backend ;; web) echo web ;; esac; }
group_images() { case "$1" in services) echo "ingest scoring api notify migrate" ;; web) echo web ;; esac; }

# The newTag line right after "- name: ghcr.io/timkrebs/ryft/<image>"; empty if there is none.
tag_of() {
  awk -v want="ghcr.io/timkrebs/ryft/$1" '
    found { if (match($0, /newTag:[ \t]*/)) { t = substr($0, RSTART + RLENGTH); gsub(/["\047 \t\r]/, "", t); print t } found = 0; next }
    { l = $0; sub(/^[ \t]*-[ \t]*name:[ \t]*/, "", l); sub(/[ \t\r]+$/, "", l) }
    $0 ~ /^[ \t]*-[ \t]*name:/ && l == want { found = 1 }
  ' "$overlay"
}

# The pins of a group's images, de-duplicated, comma-joined (one value when they agree);
# "none" when an image has no pin at all.
pins_of() {
  local img t all=
  for img in $(group_images "$1"); do
    t=$(tag_of "$img")
    [ -n "$t" ] || { echo none; return; }
    all="$all$t"$'\n'
  done
  printf '%s' "$all" | sort -u | paste -sd, -
}

hold_like='#[[:space:]]*hold'
hold_ok='^[[:space:]]*#[[:space:]]hold:[[:space:]](services|web)[[:space:]]([0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01]))[[:space:]]+[^[:space:]].*$'

# Every hold-looking line must parse. Exit 2 otherwise.
validate_holds() {
  local bad
  bad=$(grep -n -i -E "$hold_like" "$overlay" | grep -v -E "^[0-9]+:${hold_ok#^}" || true)  # a valid hold is a whole line
  if [ -n "$bad" ]; then
    printf '%s\n' "$bad" | while IFS= read -r l; do
      echo "::error file=$overlay,line=${l%%:*},title=hold::not a hold: '${l#*:}'. A hold reads '# hold: <services|web> <YYYY-MM-DD> <reason>'; a line that starts like one but does not parse stops the promote on purpose." >&2
    done
    exit 2
  fi
}

# "<date> <reason>" of the group's hold line, or return 1 when there is none.
hold_of() {
  local line
  line=$(grep -E "$hold_ok" "$overlay" | grep -E "^[[:space:]]*#[[:space:]]hold:[[:space:]]$1[[:space:]]" | head -1) || return 1
  [ -n "$line" ] || return 1
  line=${line#*hold:}; line=${line#"${line%%[![:space:]]*}"}; line=${line#"$1"}; line=${line#"${line%%[![:space:]]*}"}
  printf '%s' "$line"
}

emit() { printf '%s=%s\n' "$1" "$2"; }

validate_holds

if [ "$mode" = check ]; then
  holds_now=
  for g in services web; do
    if h=$(hold_of "$g"); then holds_now="$holds_now$g"$'\n'; fi
  done
  {
    echo "### Holds in $overlay"
    if [ -z "$holds_now" ]; then echo "none"; else
      for g in services web; do
        if h=$(hold_of "$g"); then echo "- **$g** held since ${h%% *}: ${h#* }"; fi
      done
    fi
  } | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}" >/dev/null
  rc=0
  # The image entries: exactly name + a quoted 7-hex newTag, nothing else.
  entries=$(awk '
    /^[ \t]*#/ { next }
    state == 1 { if ($0 ~ /^    newTag: "[0-9a-f]{7}"[ \t]*$/) { state = 2; next } print NR ": " name " is not followed by newTag: \"<7 hex>\""; state = 0 }
    state == 2 { state = 0; if ($0 ~ /^    [^ #]/) { print NR ": " name " has a further key: " $0; next } }
    /ghcr\.io\/timkrebs\/ryft\// {
      if ($0 ~ /^  - name: ghcr\.io\/timkrebs\/ryft\/[a-z-]+[ \t]*$/) { name = $0; sub(/.*ryft\//, "", name); state = 1; next }
      print NR ": not a plain image entry: " $0
    }
    /^[ \t]+(newName|digest):/ { print NR ": " $0 }
  ' "$overlay")
  if [ -n "$entries" ]; then
    printf '%s\n' "$entries" | while IFS= read -r l; do echo "::error file=$overlay,title=image entry::$l" >&2; done
    rc=1
  fi
  if [ -n "$base" ]; then
    if git rev-parse -q --verify "$base^{commit}" >/dev/null; then
      head_overlay=$overlay; base_overlay=$(mktemp); trap 'rm -f "$base_overlay"' EXIT
      git show "$base:$overlay" > "$base_overlay"
      for g in services web; do
        overlay=$base_overlay; base_hold=; base_pins=
        if hold_of "$g" >/dev/null; then base_hold=1; fi
        base_pins=$(pins_of "$g")
        overlay=$head_overlay; head_hold=; head_pins=
        if hold_of "$g" >/dev/null; then head_hold=1; fi
        head_pins=$(pins_of "$g")
        if [ -z "$base_hold" ] && [ -n "$head_hold" ]; then echo "::notice title=hold::this PR holds $g: promote will not deploy it until the hold line is removed."; fi
        if [ -n "$base_hold" ] && [ -z "$head_hold" ]; then echo "::notice title=hold::this PR releases the hold on $g: its merge deploys main's head for $g."; fi
        if [ -n "$base_hold" ] && [ "$base_pins" != "$head_pins" ] && printf '%s' "${HEAD_REF:-}" | grep -Eq '^deploy/[0-9a-f]{7}$'; then
          echo "::error file=$overlay,title=held group::$g is held on $base and this PR changes its pin ($base_pins -> $head_pins). Release the hold first (a PR that only removes the line), or roll back and hold in one PR before the hold exists." >&2
          rc=1
        fi
      done
    else
      echo "deploy_targets: base '$base' not found: is the checkout complete (fetch-depth: 0)?" >&2; rc=1
    fi
  fi
  exit "$rc"
fi

for g in services web; do
  dir=$(group_dir "$g"); pin=$(pins_of "$g"); target=false; why=
  forced=
  case "$force" in all|"$g") forced=1 ;; esac
  if hold=$(hold_of "$g"); then
    why="held since ${hold%% *}: ${hold#* }"
    if git rev-parse -q --verify "$pin^{commit}" >/dev/null 2>&1; then
      if git diff --quiet "$pin" HEAD -- "$dir"; then pending="no change under $dir/ since $pin"
      else pending="$(git rev-list --count "$pin..HEAD" -- "$dir") commits under $dir/ since $pin"; fi
    else
      pending="$dir/ against the pin $pin is unknown"
    fi
    echo "::warning title=$g held::$g held since ${hold%% *} (${hold#* }), $pending, not deployed. Remove the '# hold: $g' line in $overlay to release it." >&2
    [ -z "$forced" ] || echo "deploy_targets: FORCE=$force ignored for $g: a hold wins" >&2
  elif [ -n "$forced" ]; then
    target=true; why="forced by workflow_dispatch"
  elif [ "$pin" = none ]; then
    target=true; why="no pin in the overlay"
  elif [ "$g" = services ] && [ "${pin#*,}" != "$pin" ]; then
    target=true; why="service pins disagree ($pin)"
  elif ! git rev-parse -q --verify "$pin^{commit}" >/dev/null 2>&1; then
    target=true; why="pin not in this history"
  elif git diff --quiet "$pin" HEAD -- "$dir"; then
    why="$dir/ unchanged since the pin"
  else
    target=true; why="$dir/ changed since the pin"
  fi
  emit "$g" "$target"
  emit "${g}_pin" "$pin"
  emit "${g}_why" "$why"
done

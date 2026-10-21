#!/usr/bin/env bash
# Which image groups does a push to main have to build and pin? (D107; docs/OPERATIONS.md
# → Deploy and roll back.) Used by CI's `changes` job and by promote.sh, so both read the
# same tree and agree. Prints key=value lines, ready for $GITHUB_OUTPUT:
#   services=false  services_pin=0fd4256  services_why=…
#   web=true        web_pin=f437747       web_why=pin not in this history
# Groups: services (ingest, scoring, api, notify, migrate: from backend/) and web (web:
# from web/). The pins are read from deploy/k8s/overlays/eks/kustomization.yaml at HEAD.
# A group is targeted when, in this order:
#   1. it is held (`# hold: <group> <reason>` in the overlay)  → NOT targeted, a warning
#   2. FORCE names it (services | web | all)                    → targeted
#   3. its pins disagree (services only) or are missing         → targeted
#   4. its pin is no commit of this history (a ryft-web SHA, a typo) → targeted
#   5. its directory differs between the pin and HEAD           → targeted, else not
# The comparison is with the RUNNING pin, not with the previous commit: a change whose run
# was cancelled or whose deploy PR was superseded stays "not deployed yet". The merged
# pin commit changes neither directory, so it targets nothing.
#   FORCE  none (default) | services | web | all  (CI's workflow_dispatch input)
# Needs the full history (fetch-depth: 0). bash 3.2-safe (macOS).
set -euo pipefail
cd "$(dirname "$0")/.."
overlay=deploy/k8s/overlays/eks/kustomization.yaml
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

hold_reason() {
  # "# hold: <group> <reason>" → the reason (may be empty); returns 1 when there is no hold.
  local line
  line=$(grep -E "^[[:space:]]*#[[:space:]]*hold:[[:space:]]+$1([[:space:]]|\$)" "$overlay" | head -1) || return 1
  line=${line#*hold:}; line=${line#"${line%%[![:space:]]*}"}; line=${line#"$1"}; line=${line#"${line%%[![:space:]]*}"}
  printf '%s' "$line"
}

emit() { printf '%s=%s\n' "$1" "$2"; }

for g in services web; do
  dir=$(group_dir "$g"); pin=$(pins_of "$g"); target=false; why=
  forced=
  case "$force" in all|"$g") forced=1 ;; esac
  if reason=$(hold_reason "$g"); then
    why="held: ${reason:-no reason given}"
    pending=
    if git rev-parse -q --verify "$pin^{commit}" >/dev/null 2>&1; then
      pending="$(git rev-list --count "$pin..HEAD" -- "$dir") commits under $dir/ since $pin"
      git diff --quiet "$pin" HEAD -- "$dir" && pending="no change under $dir/ since $pin"
    else
      pending="$dir/ against the pin $pin is unknown"
    fi
    echo "::warning title=$g held::$g held (${reason:-no reason given}), $pending, not deployed. Remove the '# hold: $g' line in $overlay to release it." >&2
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

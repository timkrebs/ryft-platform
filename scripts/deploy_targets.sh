#!/usr/bin/env bash
# Which image groups does a push to main have to build and pin? (D107; docs/OPERATIONS.md
# → Deploy and roll back.) Used by CI's `changes` job and by promote.sh, so both read the
# same tree and agree. Prints key=value lines, ready for $GITHUB_OUTPUT:
#   services=false  services_pin=0fd4256  services_why=…
#   web=true        web_pin=f437747       web_why=pin not in this history
#   held=false      (true when any group is held)
# Groups: services (ingest, scoring, api, notify, migrate: from backend/) and web (web:
# from web/). The pins are read from deploy/k8s/overlays/eks/kustomization.yaml at HEAD.
# A group is targeted when, in this order:
#   1. it is held (`# hold: <group> <YYYY-MM-DD> <reason>` in the overlay) → NOT targeted, a warning
#   2. FORCE names it (services | web | all)                    → targeted
#   3. its pins disagree (services only) or one is missing       → targeted
#   4. its pin is no commit of this history (a ryft-web SHA, a typo) → targeted
#   5. its directory differs between the pin and HEAD           → targeted, else not
# The comparison is with the RUNNING pin, not with the previous commit: a change whose run
# was cancelled or whose deploy PR was superseded stays "not deployed yet". The merged
# pin commit changes neither directory, so it targets nothing.
# The overlay is ASCII: any byte other than tab, newline and printable ASCII stops the script
# (exit 2), in every mode. A CR, U+2028, U+2029 or U+0085 inside a comment is invisible to
# the line-based reading here but a line break to the YAML parser kustomize uses, so a "hold
# reason" could smuggle an `images:` entry (a newName) past every check.
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
# the base is not held yet; a PR that only removes the hold passes.) It also reads the whole
# overlay as an ALLOWLIST: comments and blank lines, the top-level keys apiVersion, kind,
# resources, patches, images and configMapGenerator once each (anything else at column 0,
# a second `images:`, a flow-style or quoted key, a `---`, fails), resources and patches as
# `- ../../services/<name>` / `- <file>.yaml` and `- path: <file>.yaml` lines (no inline patch, no remote base, no ../), configMapGenerator as the two
# in-force PRIVACY_POLICY_VERSION literals, and under images exactly the six entries
# `- name: ghcr.io/timkrebs/ryft/<ingest|scoring|api|notify|migrate|web>` + `newTag: "<7 hex>"`,
# each once: a newName, digest or any further line would silently change what runs.
#   HEAD_REF  the pull request's head branch (github.head_ref)
#
#   scripts/deploy_targets.sh --reason "<text>"
# Whether a hold's reason is fit to be public, with no overlay needed: it lands in the overlay,
# in deploy PR bodies and in run summaries, so no `@` (it would ping or link an account) and no
# GitHub closing keyword (close/fix/resolve, any inflection) right before an issue reference
# (`#12`, `org/repo#12`, `GH-12`, or an issues URL): the deploy PR's merge would close the
# issue. Exit 2 with the reason why, else 0. The same rules run on every hold line in the
# overlay (every mode), so a hand-edited line is checked as well; hold.sh calls this mode.
#
#   scripts/deploy_targets.sh --pins
# The overlay's pins as `<image>=<newTag>` lines (empty tag: no pin), for check_registry.sh and
# hold.sh, which compare them with what kustomize renders and with each other.
# Needs the full history (fetch-depth: 0). bash 3.2-safe (macOS).
set -euo pipefail
cd "$(dirname "$0")/.."
overlay=deploy/k8s/overlays/eks/kustomization.yaml
mode=targets; base=
if [ "${1:-}" = --check ]; then mode=check; base=${2:-}; fi
if [ "${1:-}" = --pins ]; then mode=pins; fi
if [ "${1:-}" = --reason ]; then mode=reason; fi

# A hold's reason is public text (see the header). Prints why on stderr and returns 1 when it is not fit.
reason_problem() { # <reason> [<line number in the overlay>]
  local where=
  [ -z "${2:-}" ] || where=",file=$overlay,line=$2"
  case "$1" in
    *@*) echo "::error title=hold reason$where::a hold reason must not contain @ (it would ping or link an account); name an issue number, nothing personal" >&2; return 1 ;;
  esac
  if printf '%s' "$1" | grep -Eiq '(^|[^[:alnum:]])(close[sd]?|fix(es|ed)?|resolve[sd]?)[^[:alnum:]]+(([[:alnum:]_.-]+/[[:alnum:]_.-]+)?#[0-9]+|GH-[0-9]+|(https?://)?([[:alnum:]-]+\.)?github\.com/[^[:space:]]*/(issues|pull)/[0-9]+)'; then
    echo "::error title=hold reason$where::a hold reason must not say 'fixes #N' (or close/resolve, with a #N, GH-N or an issue URL): GitHub would close the issue when the deploy PR merges. Write 'see #N'." >&2; return 1
  fi
  return 0
}
if [ "$mode" = reason ]; then
  [ "$#" = 2 ] || { echo "usage: deploy_targets.sh --reason \"<text>\"" >&2; exit 2; }
  reason_problem "$2" || exit 2
  exit 0
fi
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
  local bad odd
  odd=$(LC_ALL=C grep -a -n -v -E "^[ -~$(printf '\t')]*\$" "$overlay" | LC_ALL=C cut -d: -f1 | head -5 | paste -sd, -) || true
  if [ -n "$odd" ]; then
    echo "::error file=$overlay,line=${odd%%,*},title=overlay bytes::$overlay (line $odd) has a byte that is not printable ASCII, tab or newline: a CR (CRLF line endings), U+2028, U+2029 or U+0085 is a line break to YAML but not to this check. Retype the line in plain ASCII." >&2
    exit 2
  fi
  bad=$(grep -n -i -E "$hold_like" "$overlay" | grep -v -E "^[0-9]+:${hold_ok#^}" || true)  # a valid hold is a whole line
  if [ -n "$bad" ]; then
    printf '%s\n' "$bad" | while IFS= read -r l; do
      echo "::error file=$overlay,line=${l%%:*},title=hold::not a hold: '${l#*:}'. A hold reads '# hold: <services|web> <YYYY-MM-DD> <reason>'; a line that starts like one but does not parse stops the promote on purpose." >&2
    done
    exit 2
  fi
  # The reasons are public text: hand-edited lines are held to the rules hold.sh applies.
  local l bad_reason=
  while IFS= read -r l; do
    [ -n "$l" ] || continue
    reason_problem "$(printf '%s' "${l#*:}" | sed -E 's/^[[:space:]]*#[[:space:]]hold:[[:space:]](services|web)[[:space:]][0-9-]{10}[[:space:]]+//')" "${l%%:*}" || bad_reason=1
  done <<EOT
$(grep -n -E "$hold_ok" "$overlay" || true)
EOT
  [ -z "$bad_reason" ] || exit 2
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

if [ "$mode" = pins ]; then
  for g in services web; do for img in $(group_images "$g"); do emit "$img" "$(tag_of "$img")"; done; done
  exit 0
fi

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
  # The whole overlay as an allowlist (see the header): every line is a comment, a blank, a known
  # top-level key, or a line of exactly the shape its section allows. Anything else is reported.
  entries=$(awk '
    function bad(m) { print NR ": " m }
    function endname() { if (pend) { bad(name " is not followed by newTag: \"<7 hex>\""); pend = 0 } }
    /^[ \t]*$/ { next }
    /^[ \t]*#/ { next }
    /^[^ \t]/ {
      endname(); sec = ""
      if ($0 == "apiVersion: kustomize.config.k8s.io/v1beta1") k = "apiVersion"
      else if ($0 == "kind: Kustomization") k = "kind"
      else if ($0 ~ /^(resources|patches|images|configMapGenerator):[ \t]*$/) { k = $0; sub(/:.*/, "", k); sec = k }
      else { bad("not an allowed top-level line (apiVersion, kind, resources, patches, images, configMapGenerator; each a plain key): " $0); next }
      if (seen[k]++) bad("the key " k " appears twice")
      next
    }
    sec == "resources" { if ($0 !~ /^  - (\.\.\/\.\.\/services\/[a-z]+|[A-Za-z0-9_-]+\.yaml)[ \t]*$/) bad("resources: not `- ../../services/<name>` or `- <file>.yaml` (a remote base or a path out of the directory would render other code): " $0); next }
    sec == "patches"   { if ($0 !~ /^  - path: [A-Za-z0-9_-]+\.yaml[ \t]*$/) bad("patches: not a plain `- path: <file>.yaml` (no inline patch, no ../): " $0); next }
    sec == "configMapGenerator" {
      if ($0 !~ /^  - \{ name: ryft-config, namespace: ryft-(api|ingest), behavior: merge, literals: \[PRIVACY_POLICY_VERSION=[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]\] \}[ \t]*$/) bad("configMapGenerator: not an in-force PRIVACY_POLICY_VERSION literal for ryft-api or ryft-ingest: " $0)
      next
    }
    sec == "images" {
      if (pend) {
        pend = 0
        if ($0 ~ /^    newTag: "[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]"[ \t]*$/) next
        bad(name " is not followed by newTag: \"<7 hex>\" (found: " $0 ")")
      }
      if ($0 ~ /^  - name: ghcr\.io\/timkrebs\/ryft\/(ingest|scoring|api|notify|migrate|web)[ \t]*$/) {
        name = $0; sub(/.*ryft\//, "", name); sub(/[ \t]+$/, "", name)
        if (names[name]++) bad("image " name " is listed twice")
        pend = 1; next
      }
      bad("images: not a plain entry of one of the six ryft images (a newName, digest, other image or other shape): " $0); next
    }
    { bad("a line outside every allowed section: " $0) }
    END {
      endname()
      if (!seen["images"]) print "0: the overlay has no images: key"
      split("ingest scoring api notify migrate web", six, " ")
      for (i = 1; i <= 6; i++) if (!names[six[i]]) print "0: no images entry for " six[i]
    }
  ' "$overlay")
  if [ -n "$entries" ]; then
    printf '%s\n' "$entries" | while IFS= read -r l; do echo "::error file=$overlay,title=overlay::$l" >&2; done
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

any_hold=false
for g in services web; do
  dir=$(group_dir "$g"); pin=$(pins_of "$g"); target=false; why=
  forced=
  case "$force" in all|"$g") forced=1 ;; esac
  if hold=$(hold_of "$g"); then
    any_hold=true
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
# CI's promote also runs when only a hold is active: it closes older deploy PRs that predate it.
emit held "$any_hold"

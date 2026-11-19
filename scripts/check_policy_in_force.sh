#!/usr/bin/env bash
# shellcheck disable=SC2043  # the language loops have one entry (English only, D230) and stay loops for German
# CI's manifests job (it needs the git history): the privacy policy version in force on Cloud Run
# has a published text (D78, F1, ticket B2). deploy/run/config.env is the version's home
# (scripts/cloudrun_render.py hands it to api and ingest; the merge's deploy.yml puts it into force).
# It fails when
#  - the file is missing, or PRIVACY_POLICY_VERSION is not set exactly once as a bare, real
#    YYYY-MM-DD (quotes, spaces and comments are part of a value; Go refuses 2026-11-31),
#  - the version is not in web/content/legal/privacy-versions.json, or has no (English) page in this
#    tree (the site is English only, D230),
#  - given a base ref (a pull request; it must resolve and hold deploy/run/config.env): the version
#    changes to one whose page is not status: published (frontmatter read like the site does; only
#    exactly "published" passes; going back to a version listed before the one on base stays
#    allowed), or base's version had a published page that this change sets back to a draft.
#   scripts/check_policy_in_force.sh [base-ref]        e.g. origin/main
# The EKS half (the overlay's literals, the pinned web image, the k8s copies) went with D3 (D246).
# A base without deploy/run/config.env fails: every base since B2 has it, and the draft guard
# cannot compare with nothing.
set -euo pipefail
cd "$(dirname "$0")/.."
base=${1:-}
list=web/content/legal/privacy-versions.json
run_cfg=deploy/run/config.env
err() { echo "::error title=policy in force::$*" >&2; exit 1; }

# The draft guard's reading of a page. The frontmatter is read the way the site reads it
# (gray-matter): a BOM, CRLF, quotes on key or value, a trailing comment and spaces after the dashes
# are all normalised, and anything that is not exactly "published" refuses.
published() { # stdin: a page; exit 0 only when its frontmatter status is exactly published
  # Normalised: BOM, CRLF, one pair of matching quotes around key or value, a trailing comment and
  # blanks at the ends. Fail closed on purpose, even for a published text: a tag (!!str), flow
  # frontmatter, a block scalar, a duplicate status key and a continuation line under status.
  LC_ALL=C awk -v sq="'" -v dq='"' '
    BEGIN { re = "^(status|" dq "status" dq "|" sq "status" sq ")[ \t]*:" }
    { sub(/\r$/, "") }
    NR == 1 { sub(/^\357\273\277/, ""); if ($0 !~ /^---[ \t]*$/) exit }
    NR > 1 && /^---[ \t]*$/ { exit }
    after { if ($0 ~ /^[ \t]*$/ || $0 ~ /^[ \t]*#/) next; if ($0 ~ /^[ \t]+[^ \t]/) bad = 1; after = 0 }  # a blank or comment line keeps the continuation open
    NR > 1 && $0 ~ re {
      s = $0; sub(/^[^:]*:[ \t]*/, "", s); sub(/[ \t]+#.*$/, "", s); sub(/[ \t]+$/, "", s)
      c = substr(s, 1, 1)
      if (length(s) >= 2 && (c == sq || c == dq) && substr(s, length(s), 1) == c) s = substr(s, 2, length(s) - 2)
      if (seen++) bad = 1
      st = s; after = 1
    }
    END { exit !(st == "published" && !bad) }'
}
listed_before() { # <candidate> <current>: the candidate is listed before the current one
  jq -e --arg a "$1" --arg b "$2" '(index($a)) as $i | (index($b)) as $j | $i != null and $j != null and $i < $j' "$list" >/dev/null
}

[ -f "$list" ] || err "$list is missing"
[ -f "$run_cfg" ] || err "$run_cfg is missing: it holds PRIVACY_POLICY_VERSION for api and ingest on Cloud Run"
n_keys=$(grep -cE '^[[:space:]]*PRIVACY_POLICY_VERSION([^A-Za-z0-9_]|$)' "$run_cfg" || true)
[ "$n_keys" = 1 ] || err "$run_cfg sets PRIVACY_POLICY_VERSION ${n_keys:-0} times: exactly once"
rv=$(sed -n 's/^PRIVACY_POLICY_VERSION=//p' "$run_cfg")
printf '%s' "$rv" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' \
  || err "$run_cfg: PRIVACY_POLICY_VERSION '$rv' is not a bare YYYY-MM-DD date (the line is exactly PRIVACY_POLICY_VERSION=YYYY-MM-DD: no quotes, spaces or comment)"
python3 -c 'import datetime,sys; datetime.date.fromisoformat(sys.argv[1])' "$rv" 2>/dev/null \
  || err "$run_cfg: PRIVACY_POLICY_VERSION '$rv' is not a real calendar date (the services refuse it at start-up)"
jq -e --arg v "$rv" 'index($v) != null' "$list" >/dev/null || err "$run_cfg: $rv is not in $list ($(jq -c . "$list"))"
for lang in en; do
  [ -f "web/content/legal/$lang/privacy/$rv.mdx" ] \
    || err "$run_cfg: $rv has no $lang page: web/content/legal/$lang/privacy/$rv.mdx is missing (the site serves the text people consent to)"
done

# Draft guard, on a pull request (a base ref is given). A version may become the one in force only
# when its (English) page says status: published. A version listed BEFORE base's was in force before,
# so going back to it is allowed.
if [ -n "$base" ]; then
  git rev-parse -q --verify "$base^{commit}" >/dev/null 2>&1 || err "the base ref '$base' does not resolve: the draft guard cannot compare (fetch it, or pass origin/<base branch>)"
  git cat-file -e "$base:$run_cfg" 2>/dev/null || err "$run_cfg does not exist on $base: the draft guard cannot tell what was in force there"
  rold=$(git show "$base:$run_cfg" | sed -n 's/^PRIVACY_POLICY_VERSION=//p' | head -1)
  [ -n "$rold" ] || err "$run_cfg on $base sets no PRIVACY_POLICY_VERSION: the draft guard cannot tell what was in force there"
  if [ "$rold" != "$rv" ] && ! listed_before "$rv" "$rold"; then
    for lang in en; do
      published < "web/content/legal/$lang/privacy/$rv.mdx" \
        || err "$run_cfg changes PRIVACY_POLICY_VERSION from $rold (on $base) to $rv, and the $lang page web/content/legal/$lang/privacy/$rv.mdx is not status: published. Publish the text first (the user's legal call), or go back to a version listed before $rold."
    done
  fi
  # An in-place un-publish: a page that is published on base for base's version must stay published.
  for lang in en; do
    if git show "$base:web/content/legal/$lang/privacy/$rold.mdx" 2>/dev/null | published \
      && ! published < "web/content/legal/$lang/privacy/$rold.mdx"; then
      err "the $lang page of $rold ($run_cfg on $base) is status: published there and is not in this change: a published policy text cannot go back to a draft in place. Publish a new version instead."
    fi
  done
fi
echo "policy in force: $run_cfg $rv (a real date, listed, its English page${base:+; against $base: no draft put into force, no published text set back to a draft})"

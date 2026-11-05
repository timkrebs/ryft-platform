#!/usr/bin/env bash
# Is this change limited to what promote writes? CI's `changes` job asks it of a pull request, to
# skip the checks a deploy PR cannot affect (the `admission` job: a pinned image tag does not
# change what the cluster admits, and a kind run per deploy would cost minutes of every release).
#   scripts/promote_only.sh <base-sha> <head-sha>      exit 0: only promote's lines; 1: anything else
# "Promote's lines" are exactly what scripts/pin_images.sh and scripts/hold.sh write into the EKS
# overlay (deploy/k8s/overlays/eks/kustomization.yaml, the only file that may change):
#   - a `newTag: "<7 hex>"` line;
#   - a `# hold: <services|web> <YYYY-MM-DD> <reason>` line, the reason printable ASCII;
#   - the PRIVACY_POLICY_VERSION literals of api and ingest, in the shape pin_images.sh keeps.
# Every added or removed line must have one of these shapes, and the file's mode must not change.
# The shapes are literal: the indentation the overlay has (4 spaces for a pin, 2 for the others),
# spaces, not tabs or other blanks (the C locale, so a range such as [ -~] is ASCII and nothing else).
# A pin or a policy literal changes a line (one removed, one added), so each hunk must remove as many of
# each as it adds; a hold line may only appear or disappear.
# A new patch, a changed image name, a new resource, a comment: not promote's, so the checks run.
# A person's rollback (old pins plus a hold) has the same shape and is skipped as well; what such a
# change can break (a tag that is no image) is the `manifests` job's registry check, not admission.
# Reads git only (diff of base...head, the merge base as GitHub shows a PR). Fails closed: any error
# is "not promote only".
set -uo pipefail
export LC_ALL=C
base=${1:?usage: promote_only.sh <base-sha> <head-sha>}
head=${2:?usage: promote_only.sh <base-sha> <head-sha>}
overlay=deploy/k8s/overlays/eks/kustomization.yaml

# One merge base, as GitHub shows a pull request: with several (a criss-cross merge) `base...head` picks
# one silently, and the diff it prints may not be what the merge changes. Several, or none, is "no".
nbases=$(git merge-base --all "$base" "$head" 2>/dev/null | wc -l | tr -d ' ')
[ "$nbases" = 1 ] || { echo "promote_only: no ($nbases merge bases: the diff would pick one)"; exit 1; }

files=$(git diff --name-only --no-renames "$base...$head" 2>/dev/null) || { echo "promote_only: no (git diff failed)"; exit 1; }
[ "$files" = "$overlay" ] || { echo "promote_only: no (changed files are not only the EKS overlay)"; exit 1; }
modes=$(git diff --raw --no-renames "$base...$head" -- "$overlay" | awk '{print $1, $2}')
[ "$modes" = ":100644 100644" ] || { echo "promote_only: no (the overlay's mode changed or it is not a plain file)"; exit 1; }

hold_re='^  # hold: (services|web) ([0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])) +[!-~][ -~]*$'
tag_re='^    newTag: "[0-9a-f]{7}"$'
policy_re='^  - \{ name: ryft-config, namespace: ryft-(api|ingest), behavior: merge, literals: \[PRIVACY_POLICY_VERSION=([0-9]{4}-[0-9]{2}-[0-9]{2})\] \}$'

# valid_date YYYY-MM-DD: a day that exists (the shape alone lets 2026-02-31 through).
valid_date() {
  local y=$((10#${1%%-*})) r=${1#*-} m d max
  m=$((10#${r%%-*})) d=$((10#${r#*-}))
  case $m in 2) max=28; if (( y % 4 == 0 && (y % 100 != 0 || y % 400 == 0) )); then max=29; fi ;; 4|6|9|11) max=30 ;; *) max=31 ;; esac
  [ "$m" -ge 1 ] && [ "$m" -le 12 ] && [ "$d" -ge 1 ] && [ "$d" -le "$max" ]
}
# At most one hold line per image group in the file as it will be (hold.sh writes one, above the group's first image).
headfile=$(git show "$head:$overlay" 2>/dev/null) || { echo "promote_only: no (cannot read the overlay at head)"; exit 1; }
for g in services web; do
  [ "$(printf '%s\n' "$headfile" | grep -cE "^[[:space:]]*#[[:space:]]*hold:[[:space:]]*$g([[:space:]]|\$)")" -le 1 ] || { echo "promote_only: no (more than one hold line for $g)"; exit 1; }
done

# The patch is read in two states. BEFORE the first `@@` only the four header lines git writes for a
# plain content change of this one file may appear, each exactly (a mode, rename, similarity, "Binary
# files" or any other header line is not promote's). AFTER it every line is hunk content: `@@ ` a new
# hunk, `\ No newline` a marker, a line starting with + or - a changed line checked against the shapes
# above (a content line `++ x`, shown as `+++ x`, or `-- x`, shown as `--- x`, is a changed line like any
# other, never a header). Each hunk must also balance its own pins and policy literals (-U0: promote
# rewrites a pin in place, so a hunk that only adds a pin, or moves one to another image, is refused).
diff=$(git diff -U0 --no-color --no-ext-diff --no-textconv --no-renames --src-prefix=a/ --dst-prefix=b/ "$base...$head" -- "$overlay") || { echo "promote_only: no (git diff failed)"; exit 1; }
hdr_diff="diff --git a/$overlay b/$overlay"
hdr_index_re='^index [0-9a-f]+\.\.[0-9a-f]+ 100644$'
hdr_old="--- a/$overlay"
hdr_new="+++ b/$overlay"
n=0 tag_add=0 tag_del=0 pol_add=0 pol_del=0 api_add=0 api_del=0 ing_add=0 ing_del=0
h_tag=0 h_pol=0 inhunk=0 seen_hdr=0
unbalanced() { echo "promote_only: no (a hunk does not replace each pin or policy literal in place)"; exit 1; }
badhdr() { echo "promote_only: no (unexpected diff header)"; exit 1; }
while IFS= read -r line; do
  if [ "$inhunk" = 0 ]; then
    case $seen_hdr in
      0) [ "$line" = "$hdr_diff" ] || badhdr ;;
      1) [[ $line =~ $hdr_index_re ]] || badhdr ;;
      2) [ "$line" = "$hdr_old" ] || badhdr ;;
      3) [ "$line" = "$hdr_new" ] || badhdr ;;
      *) case "$line" in '@@ '*) inhunk=1 ;; *) badhdr ;; esac ;;
    esac
    seen_hdr=$((seen_hdr + 1))
    continue
  fi
  case "$line" in
    '@@ '*) { [ "$h_tag" = 0 ] && [ "$h_pol" = 0 ]; } || unbalanced; h_tag=0 h_pol=0 ;;
    '\ No newline'*) echo "promote_only: no (a newline at the end of the file changed)"; exit 1 ;;
    +*|-*) sign=${line%"${line#?}"}; body=${line#?}
      if [[ $body =~ $tag_re ]]; then
        if [ "$sign" = + ]; then tag_add=$((tag_add + 1)); h_tag=$((h_tag + 1)); else tag_del=$((tag_del + 1)); h_tag=$((h_tag - 1)); fi
      elif [[ $body =~ $policy_re ]]; then
        valid_date "${BASH_REMATCH[2]}" || { echo "promote_only: no (a policy version that is not a calendar date)"; exit 1; }
        if [ "${BASH_REMATCH[1]}" = api ]; then k=api; else k=ing; fi
        if [ "$sign" = + ]; then pol_add=$((pol_add + 1)); h_pol=$((h_pol + 1)); eval "${k}_add=\$((${k}_add + 1))"; else pol_del=$((pol_del + 1)); h_pol=$((h_pol - 1)); eval "${k}_del=\$((${k}_del + 1))"; fi
      elif [[ $body =~ $hold_re ]]; then
        valid_date "${BASH_REMATCH[2]}" || { echo "promote_only: no (a hold with a date that does not exist)"; exit 1; }
        case $body in *'$('*|*'`'*) echo "promote_only: no (a hold reason with a command substitution)"; exit 1 ;; esac
      else
        echo "promote_only: no (a line is not one promote writes)"; exit 1
      fi
      n=$((n + 1)) ;;
    *) echo "promote_only: no (unexpected diff line)"; exit 1 ;;
  esac
done <<EOF
$diff
EOF
{ [ "$h_tag" = 0 ] && [ "$h_pol" = 0 ]; } || unbalanced
[ "$n" -gt 0 ] || { echo "promote_only: no (no changed line)"; exit 1; }
[ "$tag_add" = "$tag_del" ] || { echo "promote_only: no ($tag_add pins added, $tag_del removed: a pin replaces a pin)"; exit 1; }
[ "$pol_add" = "$pol_del" ] || { echo "promote_only: no ($pol_add policy-version literals added, $pol_del removed: a literal replaces a literal)"; exit 1; }
# The policy literals: one line per namespace, api and ingest together (pin_images.sh moves both).
[ "$api_add" -le 1 ] && [ "$api_del" -le 1 ] && [ "$ing_add" -le 1 ] && [ "$ing_del" -le 1 ] \
  && [ "$api_add" = "$ing_add" ] && [ "$api_del" = "$ing_del" ] || { echo "promote_only: no (the policy literals are not one api line and one ingest line, each replaced once)"; exit 1; }
echo "promote_only: yes ($n changed lines, all pins, holds or policy-version literals of the EKS overlay)"

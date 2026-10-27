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
# The shapes are literal: spaces, not tabs or other blanks (the C locale, so a range such as [ -~] is
# ASCII and nothing else). A pin or a policy literal changes a line (one removed, one added), so each of
# those two shapes must be removed as often as it is added; a hold line may only appear or disappear.
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

files=$(git diff --name-only "$base...$head" 2>/dev/null) || { echo "promote_only: no (git diff failed)"; exit 1; }
[ "$files" = "$overlay" ] || { echo "promote_only: no (changed files are not only the EKS overlay)"; exit 1; }
modes=$(git diff --raw "$base...$head" -- "$overlay" | awk '{print $1, $2}')
[ "$modes" = ":100644 100644" ] || { echo "promote_only: no (the overlay's mode changed or it is not a plain file)"; exit 1; }

hold_re='^ *# hold: (services|web) [0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01]) +[!-~][ -~]*$'
tag_re='^ +newTag: "[0-9a-f]{7}"$'
policy_re='^ +- \{ name: ryft-config, namespace: ryft-(api|ingest), behavior: merge, literals: \[PRIVACY_POLICY_VERSION=[0-9]{4}-[0-9]{2}-[0-9]{2}\] \}$'

diff=$(git diff -U0 --no-color "$base...$head" -- "$overlay") || { echo "promote_only: no (git diff failed)"; exit 1; }
n=0 tag_add=0 tag_del=0 pol_add=0 pol_del=0
while IFS= read -r line; do
  case "$line" in
    '+++ '*|'--- '*|'@@ '*|'diff --git '*|'index '*) continue ;;
    '\ No newline'*) echo "promote_only: no (a newline at the end of the file changed)"; exit 1 ;;
    +*|-*) sign=${line%"${line#?}"}; body=${line#?}
      if [[ $body =~ $tag_re ]]; then
        if [ "$sign" = + ]; then tag_add=$((tag_add + 1)); else tag_del=$((tag_del + 1)); fi
      elif [[ $body =~ $policy_re ]]; then
        if [ "$sign" = + ]; then pol_add=$((pol_add + 1)); else pol_del=$((pol_del + 1)); fi
      elif [[ $body =~ $hold_re ]]; then
        :
      else
        echo "promote_only: no (a line is not one promote writes)"; exit 1
      fi
      n=$((n + 1)) ;;
    *) echo "promote_only: no (unexpected diff line)"; exit 1 ;;
  esac
done <<EOF
$diff
EOF
[ "$n" -gt 0 ] || { echo "promote_only: no (no changed line)"; exit 1; }
[ "$tag_add" = "$tag_del" ] || { echo "promote_only: no ($tag_add pins added, $tag_del removed: a pin replaces a pin)"; exit 1; }
[ "$pol_add" = "$pol_del" ] || { echo "promote_only: no ($pol_add policy-version literals added, $pol_del removed: a literal replaces a literal)"; exit 1; }
echo "promote_only: yes ($n changed lines, all pins, holds or policy-version literals of the EKS overlay)"

#!/usr/bin/env bash
# Is this change limited to what promote writes? CI's `changes` job asks it of a pull request, to
# skip the checks a deploy PR cannot affect (the `admission` job: a pinned image tag does not
# change what the cluster admits, and a kind run per deploy would cost minutes of every release).
#   scripts/promote_only.sh <base-sha> <head-sha>      exit 0: only promote's lines; 1: anything else
# "Promote's lines" are exactly what scripts/pin_images.sh and scripts/hold.sh write into the EKS
# overlay (deploy/k8s/overlays/eks/kustomization.yaml, the only file that may change):
#   - a `newTag: "<7 hex>"` line;
#   - a `# hold: <services|web> <YYYY-MM-DD> <reason>` line;
#   - the PRIVACY_POLICY_VERSION literals of api and ingest, in the shape pin_images.sh keeps.
# Every added or removed line must have one of these shapes, and the file's mode must not change.
# A new patch, a changed image name, a new resource, a comment: not promote's, so the checks run.
# A person's rollback (old pins plus a hold) has the same shape and is skipped as well; what such a
# change can break (a tag that is no image) is the `manifests` job's registry check, not admission.
# Reads git only (diff of base...head, the merge base as GitHub shows a PR). Fails closed: any error
# is "not promote only".
set -uo pipefail
base=${1:?usage: promote_only.sh <base-sha> <head-sha>}
head=${2:?usage: promote_only.sh <base-sha> <head-sha>}
overlay=deploy/k8s/overlays/eks/kustomization.yaml

files=$(git diff --name-only "$base...$head" 2>/dev/null) || { echo "promote_only: no (git diff failed)"; exit 1; }
[ "$files" = "$overlay" ] || { echo "promote_only: no (changed files are not only the EKS overlay)"; exit 1; }
modes=$(git diff --raw "$base...$head" -- "$overlay" | awk '{print $1, $2}')
[ "$modes" = ":100644 100644" ] || { echo "promote_only: no (the overlay's mode changed or it is not a plain file)"; exit 1; }

hold_re='^[[:space:]]*#[[:space:]]hold:[[:space:]](services|web)[[:space:]]([0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01]))[[:space:]]+[^[:space:]].*$'
tag_re='^[[:space:]]+newTag:[[:space:]]"[0-9a-f]{7}"$'
policy_re='^[[:space:]]+-[[:space:]]\{[[:space:]]name:[[:space:]]ryft-config,[[:space:]]namespace:[[:space:]]ryft-(api|ingest),[[:space:]]behavior:[[:space:]]merge,[[:space:]]literals:[[:space:]]\[PRIVACY_POLICY_VERSION=[0-9]{4}-[0-9]{2}-[0-9]{2}\][[:space:]]\}$'

diff=$(git diff -U0 --no-color "$base...$head" -- "$overlay") || { echo "promote_only: no (git diff failed)"; exit 1; }
n=0
while IFS= read -r line; do
  case "$line" in
    '+++ '*|'--- '*|'@@ '*|'diff --git '*|'index '*) continue ;;
    '\ No newline'*) echo "promote_only: no (a newline at the end of the file changed)"; exit 1 ;;
    +*|-*) body=${line#?}
      if [[ $body =~ $tag_re || $body =~ $hold_re || $body =~ $policy_re ]]; then
        n=$((n + 1))
      else
        echo "promote_only: no (a line is not one promote writes)"; exit 1
      fi ;;
    *) echo "promote_only: no (unexpected diff line)"; exit 1 ;;
  esac
done <<EOF
$diff
EOF
[ "$n" -gt 0 ] || { echo "promote_only: no (no changed line)"; exit 1; }
echo "promote_only: yes ($n changed lines, all pins, holds or policy-version literals of the EKS overlay)"

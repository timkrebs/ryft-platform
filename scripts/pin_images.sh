#!/usr/bin/env bash
# Pins the RYFT images of the given groups in the EKS overlay to one commit
# (docs/OPERATIONS.md → Deploy and roll back):
#   scripts/pin_images.sh <short-sha> <group>... [kustomization.yaml]
# Groups (D107): services = ingest, scoring, api, notify, migrate (built from backend/);
# web = the apex site (built from web/). A group that is not named is left byte-for-byte
# as it is. CI's promote job runs it on a deploy/<sha> branch (scripts/promote.sh) and
# opens the PR; Argo CD deploys the merge. Prints one line per group on stdout, the
# previously pinned SHA (the running release) so the PR can list the commits in between:
#   services=0fd4256        (comma-joined when the images were pinned to different commits)
set -euo pipefail
usage="usage: pin_images.sh <short-sha> <services|web>... [kustomization.yaml]"
sha=${1:?$usage}; shift
case "$sha" in
  [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
  *) echo "✗ pin_images: $sha is not a 7-digit short SHA (promote.sh cuts HEAD to 7 characters)" >&2; exit 2 ;;
esac
groups=()
file=$(cd "$(dirname "$0")/.." && pwd)/deploy/k8s/overlays/eks/kustomization.yaml
for a in "$@"; do
  case "$a" in
    services|web) groups+=("$a") ;;
    *) file=$a ;;
  esac
done
[ "${#groups[@]}" -gt 0 ] || { echo "✗ pin_images: name at least one group. $usage" >&2; exit 2; }
python3 - "$file" "$sha" "${groups[@]}" <<'PY'
import re, sys
path, sha, groups = sys.argv[1], sys.argv[2], sys.argv[3:]
members = {
    "services": ["ingest", "scoring", "api", "notify", "migrate"],
    "web": ["web"],
}
lines = open(path).read().split("\n")
for g in groups:
    images = members[g]
    old, seen = set(), set()
    for i, line in enumerate(lines):
        m = re.match(r"^\s*- name: ghcr\.io/timkrebs/ryft/([a-z-]+)\s*$", line)
        if not m or m.group(1) not in images:
            continue
        j = i + 1
        t = re.match(r'^(\s*)newTag:\s*"?([^"\s]*)"?\s*$', lines[j]) if j < len(lines) else None
        if not t:
            sys.exit(f"✗ pin_images: no newTag right after {m.group(1)} in {path}")
        old.add(t.group(2)); seen.add(m.group(1))
        lines[j] = f'{t.group(1)}newTag: "{sha}"'
    missing = set(images) - seen
    if missing:
        sys.exit(f"✗ pin_images: {', '.join(sorted(missing))} not pinned in {path}")
    if len(old) != 1:
        print(f"! pin_images: the {g} images were pinned to different commits: {sorted(old)}", file=sys.stderr)
    print(f"{g}={','.join(sorted(old))}")
open(path, "w").write("\n".join(lines))
PY

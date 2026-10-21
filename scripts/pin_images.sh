#!/usr/bin/env bash
# Pins every RYFT image in the EKS overlay to one commit (docs/OPERATIONS.md → Deploy):
#   scripts/pin_images.sh <short-sha> [kustomization.yaml]
# CI's promote job runs it on a deploy/<sha> branch and opens the PR; Argo CD deploys
# the merge. Prints the previously pinned SHA (the running release) on stdout, so the
# PR can list the commits in between. Only the five service images move together;
# the web site's pin (ghcr.io/timkrebs/ryft/web) is left as it is.
set -euo pipefail
sha=${1:?usage: pin_images.sh <short-sha> [kustomization.yaml]}
file=${2:-$(cd "$(dirname "$0")/.." && pwd)/deploy/k8s/overlays/eks/kustomization.yaml}
case "$sha" in
  [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
  *) echo "✗ pin_images: $sha is not a 7-digit short SHA (CI tags images with git rev-parse --short=7)" >&2; exit 2 ;;
esac
python3 - "$file" "$sha" <<'PY'
import re, sys
path, sha = sys.argv[1], sys.argv[2]
images = ["ingest", "scoring", "api", "notify", "migrate"]
lines = open(path).read().split("\n")
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
    print(f"! pin_images: the images were pinned to different commits: {sorted(old)}", file=sys.stderr)
open(path, "w").write("\n".join(lines))
print(sorted(old)[0])
PY

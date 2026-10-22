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
# Pinning web also moves the privacy policy version in force on EKS (the two PRIVACY_POLICY_VERSION
# literals of the overlay's configMapGenerator, api and ingest) to the version of
# deploy/k8s/base/{api,ingest}/config.env AS OF the pinned commit, so the page and the version go
# live in one sync (D78, F1). A commit from before config.env existed (before the F1 change) has
# none: the newest web/content/legal/{en,de}/privacy/*.mdx there decides, and both languages must
# agree. The one known foreign pin f437747 (built in the archived ryft-web repository) carries
# 2026-10-20. Any other sha must resolve to exactly one commit of this history, else it exits
# non-zero, as it does when the two base files disagree, the version is no real date or the overlay
# lacks the literals. stderr: "in force: A -> B" (PIN_QUIET=1 silences it). Pinning services leaves
# the literals as they are.
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
root=$(cd "$(dirname "$0")/.." && pwd)
python3 - "$file" "$root" "$sha" "${groups[@]}" <<'PY'
import os, re, sys
path, root, sha, groups = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
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
if "web" in groups:
    import datetime, json, subprocess
    # The version the pinned web image serves, as of the pinned commit (not the working tree):
    # base's config.env there; for a commit from before config.env existed, the newest privacy
    # page there (both languages must agree: that is what /privacy serves). The one known
    # foreign pin (f437747, built in the archived ryft-web repository) carries 2026-10-20.
    # Any other sha must resolve to exactly one commit of this history.
    FOREIGN = "f437747"
    FOREIGN_VERSION = "2026-10-20"  # that image never changes: it carries this version, as a draft
    def run(*a):
        return subprocess.run(["git", "-C", root, *a], capture_output=True, text=True)
    cands = run("rev-parse", "--disambiguate=" + sha).stdout.split()
    commits = [c for c in cands if run("cat-file", "-t", c).stdout.strip() == "commit"]
    if len(commits) == 1:
        full = commits[0]
        versions = {}
        for s in ("api", "ingest"):
            f = f"deploy/k8s/base/{s}/config.env"
            r = run("show", f"{full}:{f}")
            if r.returncode != 0:
                versions = None
                break
            found = re.findall(r"^PRIVACY_POLICY_VERSION=([0-9]{4}-[0-9]{2}-[0-9]{2})$", r.stdout, re.M)
            if len(found) != 1:
                sys.exit(f"✗ pin_images: {f} at {sha} needs exactly one line PRIVACY_POLICY_VERSION=YYYY-MM-DD (bare, no quotes)")
            versions[s] = found[0]
        if versions is not None:
            if versions["api"] != versions["ingest"]:
                sys.exit(f"✗ pin_images: base api has {versions['api']} and base ingest {versions['ingest']} at {sha}: they must agree")
            version = versions["api"]
        else:
            newest = {}
            for lang in ("en", "de"):
                r = run("ls-tree", "--name-only", f"{full}:web/content/legal/{lang}/privacy/")
                names = sorted(n[:-4] for n in r.stdout.split("\n") if n.endswith(".mdx"))
                if r.returncode != 0 or not names:
                    sys.exit(f"✗ pin_images: {sha} has no config.env and no {lang} privacy page: cannot tell which version it serves")
                newest[lang] = names[-1]
            if newest["en"] != newest["de"]:
                sys.exit(f"✗ pin_images: at {sha} the newest privacy page is {newest['en']} in English and {newest['de']} in German")
            version = newest["en"]
    elif not cands and sha == FOREIGN:
        version = FOREIGN_VERSION
    else:
        sys.exit(f"✗ pin_images: {sha} is not exactly one commit of this history ({len(commits)} found) and not the known foreign pin {FOREIGN}")
    try:
        datetime.date.fromisoformat(version)
        assert re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}", version)
    except (ValueError, AssertionError):
        sys.exit(f"✗ pin_images: privacy policy version '{version}' is not a real YYYY-MM-DD date")
    moved, was = 0, None
    for i, line in enumerate(lines):
        m = re.match(r"^(\s*- \{ name: ryft-config, namespace: ryft-(?:api|ingest), behavior: merge, literals: \[PRIVACY_POLICY_VERSION=)([^\]]*)(\] \}\s*)$", line)
        if m:
            was = was or m.group(2)
            lines[i] = m.group(1) + version + m.group(3); moved += 1
    if moved != 2:
        sys.exit(f"✗ pin_images: expected the two PRIVACY_POLICY_VERSION literals (api, ingest) in {path}, found {moved}")
    if not os.environ.get("PIN_QUIET"):  # the tests set it
        print(f"in force: {was} -> {version}", file=sys.stderr)
open(path, "w").write("\n".join(lines))
PY

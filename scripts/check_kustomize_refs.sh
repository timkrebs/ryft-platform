#!/usr/bin/env bash
# Part of make k8s-validate. Every kustomization.yaml under deploy/ may reference only files and
# directories of this repository below deploy/: kustomize would otherwise fetch a remote base
# (`github.com/...//path?ref=...`, `https://...`) or read `../../../backend/...`, and what CI
# renders is then not what this repository holds. For every entry of resources, bases,
# components, crds, configurations, patchesStrategicMerge, patches[].path, patchesJson6902[].path,
# replacements[].path, openapi.path and the files/envs/env of the config and secret generators:
#   - relative, no scheme (`://`), no `github.com/`, no `?ref=`, no `@`, no `~` or `$`;
#   - it exists, and its real path (symlinks followed) is inside deploy/;
#   - an entry in resources/bases/components is a directory with a kustomization.yaml or a *.yaml /
#     *.yml file (an image or a .json would be read as a manifest too); inline patches are text in
#     the kustomization itself, which scripts/check_bytes.sh covers;
#   - the inputs of the generators may be generated and git-ignored: only their location is checked;
#   - `generators`, `transformers` and `helmCharts` (they run code or fetch charts) are refused.
# Parsed with yq (mikefarah v4), so flow style and quoting read as kustomize reads them; without
# yq it fails (exit 2). Usage: scripts/check_kustomize_refs.sh [dir-below-which-to-look]  (default deploy)
set -euo pipefail
cd "$(dirname "$0")/.."
root=${1:-deploy}
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  echo "check_kustomize_refs: yq (mikefarah, v4) is not installed, so the kustomizations cannot be parsed (CI installs it; locally: brew install yq)" >&2
  exit 2
fi
errf=$(mktemp); trap 'rm -f "$errf"' EXIT
n=0; bad=0
while IFS= read -r -d '' f; do
  n=$((n+1))
  if ! json=$(yq -o=json 'explode(.)' "$f" 2>"$errf") || [ -s "$errf" ]; then
    echo "::error file=$f,title=kustomize refs::$f: yq cannot parse it cleanly: $(cat "$errf")" >&2; bad=1; continue
  fi
  if ! printf '%s' "$json" | python3 -I -c '
import json, os, re, sys
f, root = sys.argv[1], os.path.realpath(sys.argv[2])
raw = sys.stdin.read()
dec, docs, i = json.JSONDecoder(), [], 0
while i < len(raw):
    if raw[i].isspace():
        i += 1; continue
    o, i = dec.raw_decode(raw, i); docs.append(o)
errs = []
def err(m): errs.append(f"{f}: {m}")
if len(docs) != 1 or not isinstance(docs[0], dict):
    err("not exactly one mapping"); docs = [{}]
k = docs[0]
here = os.path.dirname(f)
for key in ("generators", "transformers", "helmCharts", "helmGlobals", "helmChartInflationGenerator"):
    if key in k: err(f"`{key}` is not allowed (it runs code or fetches a chart)")
def ref(entry, key, kind):  # kind: "res" (dir or .yaml), "file" (any file inside), "path" (a file)
    if not isinstance(entry, str) or not entry:
        err(f"{key}: an entry that is not a plain string: {entry!r}"); return
    e = entry
    if kind == "file" and "=" in e: e = e.split("=", 1)[1]
    if re.search(r"://|github\.com|\?ref=|@|^~|\$|^/|\\\\|:", e) or e.startswith("git::"):
        err(f"{key}: {entry!r} is not a plain relative path (remote, absolute or odd)"); return
    p = os.path.realpath(os.path.join(here, e))
    if p != root and not p.startswith(root + os.sep):
        err(f"{key}: {entry!r} resolves outside {os.path.relpath(root)}/"); return
    if not os.path.exists(p):
        # generator inputs may be generated and git-ignored (local-postgres secrets/*.env): the
        # build fails on its own for those; resources and patches must be in the repository.
        if key.startswith(("configMapGenerator", "secretGenerator")): return
        err(f"{key}: {entry!r} does not exist"); return
    if kind == "res":
        if os.path.isdir(p):
            if not os.path.isfile(os.path.join(p, "kustomization.yaml")): err(f"{key}: {entry!r} is a directory without kustomization.yaml")
        elif not p.endswith((".yaml", ".yml")): err(f"{key}: {entry!r} is neither a directory nor a *.yaml / *.yml file")
    else:
        if os.path.isdir(p): err(f"{key}: {entry!r} is a directory")
        if kind == "path" and not p.endswith(".yaml") and not p.endswith(".json"): err(f"{key}: {entry!r} is not a *.yaml file")
def lst(key):
    v = k.get(key, [])
    if v is None: return []
    if not isinstance(v, list): err(f"{key}: not a list"); return []
    return v
for key in ("resources", "bases", "components"):
    for e in lst(key): ref(e, key, "res")
for key in ("crds", "configurations"):
    for e in lst(key): ref(e, key, "file")
for e in lst("patchesStrategicMerge"):
    if isinstance(e, str) and e.endswith((".yaml", ".yml", ".json")): ref(e, "patchesStrategicMerge", "path")
    else: err(f"patchesStrategicMerge: an inline patch or odd entry: {str(e)[:60]!r}")
for key in ("patches", "patchesJson6902", "replacements"):
    for e in lst(key):
        if not isinstance(e, dict): err(f"{key}: an entry that is not a mapping"); continue
        if "path" in e: ref(e["path"], key + "[].path", "path")
if isinstance(k.get("openapi"), dict) and "path" in k["openapi"]: ref(k["openapi"]["path"], "openapi.path", "file")
for key in ("configMapGenerator", "secretGenerator"):
    for g in lst(key):
        if not isinstance(g, dict): err(f"{key}: an entry that is not a mapping"); continue
        for sub in ("files", "envs"):
            for e in g.get(sub, []) or []: ref(e, f"{key}[].{sub}", "file")
        if g.get("env"): ref(g["env"], f"{key}[].env", "file")
for m in errs: print(m)
sys.exit(1 if errs else 0)
' "$f" "$root" > "$errf" 2>&1; then
    while IFS= read -r l; do [ -z "$l" ] || echo "::error file=$f,title=kustomize refs::$l" >&2; done < "$errf"; bad=1
  fi
done < <(find "$root" \( -name kustomization.yaml -o -name kustomization.yml -o -name Kustomization \) -print0 | sort -z)
[ "$n" -gt 0 ] || { echo "check_kustomize_refs: no kustomization under $root" >&2; exit 2; }
[ "$bad" = 0 ] || exit 1
echo "✓ $n kustomizations: every resource, patch and generator file is a plain path inside $root/ that exists"

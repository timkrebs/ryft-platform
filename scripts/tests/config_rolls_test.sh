#!/usr/bin/env bash
# A service's configuration rolls its pods, and only its own (D78, F1; make k8s-validate and
# make test-scripts). Copies deploy/k8s to a temp directory, renders, changes one value, renders
# again and compares the Deployments and Jobs per namespace/name:
#  1. the EKS in-force pin of PRIVACY_POLICY_VERSION rolls exactly ryft-api/api and ryft-ingest/ingest
#  2. a change of the base version alone rolls nothing on EKS (the pin wins); services/api and
#     services/ingest, which have no pin, roll
#  3. LOG_LEVEL in base/scoring/config.env rolls only ryft-scoring/scoring
#  4. the four Deployments use ryft-config-<hash>; migrate's Job keeps the plain ryft-config
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ config_rolls_test: $*" >&2; exit 1; }
cp -R "$HERE/deploy/k8s" "$tmp/k8s"
EKS=$tmp/k8s/overlays/eks

# rolled <before.yaml> <after.yaml>: the Deployments and Jobs whose documents differ, as "Kind/ns/name".
rolled() {
  python3 - "$1" "$2" <<'PY'
import re, sys
def load(p):
    out = {}
    for d in open(p).read().split("\n---\n"):
        k = re.search(r"^kind: (Deployment|Job)$", d, re.M)
        if not k:
            continue
        n = re.search(r"^  name: (\S+)", d, re.M).group(1)
        ns = re.search(r"^  namespace: (\S+)", d, re.M).group(1)
        out[f"{k.group(1)}/{ns}/{n}"] = d
    return out
a, b = load(sys.argv[1]), load(sys.argv[2])
if a.keys() != b.keys():
    sys.exit("workloads differ: " + str(sorted(a.keys() ^ b.keys())))
print("\n".join(sorted(k for k in a if a[k] != b[k])))
PY
}
render() { kubectl kustomize "$tmp/k8s/$1" > "$2"; }
sedi() { sed -i.bak "$1" "$2"; rm -f "$2.bak"; }

render overlays/eks "$tmp/eks0.yaml"; render services/api "$tmp/api0.yaml"; render services/ingest "$tmp/ingest0.yaml"
grep -q 'kind: Deployment' "$tmp/eks0.yaml" || fail "no Deployments rendered"

# 4. who uses which ConfigMap
python3 - "$tmp/eks0.yaml" <<'PY' || fail "the ConfigMap names are wrong"
import re, sys
docs = open(sys.argv[1]).read().split("\n---\n")
want = {"api": "ryft-api", "ingest": "ryft-ingest", "scoring": "ryft-scoring", "notify": "ryft-notify"}
seen = set()
for d in docs:
    m = re.search(r"^kind: (Deployment|Job)$", d, re.M)
    if not m:
        continue
    name = re.search(r"^  name: (\S+)", d, re.M).group(1)
    ns = re.search(r"^  namespace: (\S+)", d, re.M).group(1)
    refs = re.findall(r"configMapRef:\n\s+name: (\S+)", d)
    if m.group(1) == "Deployment" and want.get(name) == ns:
        if len(refs) != 1 or not re.fullmatch(r"ryft-config-[a-z0-9]{10}", refs[0]):
            sys.exit(f"{ns}/{name} references {refs}, want ryft-config-<hash>")
        seen.add(name)
    if m.group(1) == "Job" and name == "migrate" and refs != ["ryft-config"]:
        sys.exit(f"migrate references {refs}, want the plain ryft-config (its PreSync hook runs before the sync creates a hashed name)")
if seen != set(want):
    sys.exit(f"Deployments seen: {sorted(seen)}")
PY

# 1. the in-force pin
sedi 's/PRIVACY_POLICY_VERSION=2026-10-20/PRIVACY_POLICY_VERSION=2026-11-02/' "$EKS/kustomization.yaml"
render overlays/eks "$tmp/eks1.yaml"
got=$(rolled "$tmp/eks0.yaml" "$tmp/eks1.yaml")
[ "$got" = "$(printf 'Deployment/ryft-api/api\nDeployment/ryft-ingest/ingest')" ] || fail "moving the in-force pin must roll exactly api and ingest, rolled: $got"
cp "$HERE/deploy/k8s/overlays/eks/kustomization.yaml" "$EKS/kustomization.yaml"

# 2. the base version alone
for s in api ingest; do sedi 's/^PRIVACY_POLICY_VERSION=.*/PRIVACY_POLICY_VERSION=2026-11-02/' "$tmp/k8s/base/$s/config.env"; done
render overlays/eks "$tmp/eks2.yaml"
got=$(rolled "$tmp/eks0.yaml" "$tmp/eks2.yaml")
[ -z "$got" ] || fail "a base-only version change must roll nothing on EKS (the pin holds it), rolled: $got"
render services/api "$tmp/api2.yaml"; render services/ingest "$tmp/ingest2.yaml"
[ "$(rolled "$tmp/api0.yaml" "$tmp/api2.yaml")" = "Deployment/ryft-api/api" ] || fail "services/api must roll on a base change"
[ "$(rolled "$tmp/ingest0.yaml" "$tmp/ingest2.yaml")" = "Deployment/ryft-ingest/ingest" ] || fail "services/ingest must roll on a base change"
rm -rf "$tmp/k8s"; cp -R "$HERE/deploy/k8s" "$tmp/k8s"

# 3. scoring's LOG_LEVEL
sedi 's/^LOG_LEVEL=.*/LOG_LEVEL=debug/' "$tmp/k8s/base/scoring/config.env"
render overlays/eks "$tmp/eks3.yaml"
got=$(rolled "$tmp/eks0.yaml" "$tmp/eks3.yaml")
[ "$got" = "Deployment/ryft-scoring/scoring" ] || fail "scoring's LOG_LEVEL must roll only scoring, rolled: $got"

echo "✓ config_rolls_test: the in-force pin rolls api and ingest, a base-only version nothing on EKS, scoring's LOG_LEVEL only scoring, migrate's ConfigMap stays plain"

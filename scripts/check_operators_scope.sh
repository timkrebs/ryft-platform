#!/usr/bin/env bash
# Part of make k8s-validate (offline): the RabbitMQ operators render namespace-scoped (T12-g, D97).
# A `$patch: delete` or JSON patch whose target matches nothing still renders with exit 0, so a
# renamed ClusterRole in a new release would silently bring back cluster-wide Secret reads. This
# asserts the RESULT of `kubectl kustomize deploy/rabbitmq/operators` (or the rendered file given
# as $1, for the tests):
#   - no ClusterRole grants secrets, pods, pods/exec or * (as a resource);
#   - the only ClusterRoleBinding is messaging-topology-metrics-auth-rolebinding;
#   - a Role and a RoleBinding for each operator exist in ryft-rabbitmq, the binding naming it;
#   - the containers `operator` (Cluster Operator) and `manager` (Topology Operator) each carry
#     OPERATOR_SCOPE_NAMESPACE=ryft-rabbitmq (a literal value).
# Needs yq (mikefarah v4), python3 and kubectl (without $1). Exit 1: a finding. Exit 2: no yq.
set -euo pipefail
cd "$(dirname "$0")/.."
command -v yq >/dev/null || { echo "check_operators_scope: yq is required (scripts/install_yq.sh)" >&2; exit 2; }
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
if [ $# -ge 1 ]; then cp "$1" "$tmp/render.yaml"; else
  GIT_ALLOW_PROTOCOL=file kubectl kustomize deploy/rabbitmq/operators > "$tmp/render.yaml"
fi
yq -o=json -I0 '.' "$tmp/render.yaml" > "$tmp/render.json"
python3 -I - "$tmp/render.json" <<'PY'
import json, sys

NS = "ryft-rabbitmq"
ONLY_CRB = "messaging-topology-metrics-auth-rolebinding"
CONTAINERS = {"rabbitmq-cluster-operator": "operator", "messaging-topology-operator": "manager"}
ROLES = {"rabbitmq-cluster-operator-role": "rabbitmq-cluster-operator-rolebinding",
         "messaging-topology-manager-role": "messaging-topology-manager-rolebinding"}
SENSITIVE = {"secrets", "pods", "pods/exec", "*"}

dec = json.JSONDecoder()
text = open(sys.argv[1]).read()
docs, i = [], 0
while i < len(text):
    while i < len(text) and text[i].isspace():
        i += 1
    if i >= len(text):
        break
    d, i = dec.raw_decode(text, i)
    if isinstance(d, dict):
        docs.append(d)

bad = []
def name(d): return d.get("metadata", {}).get("name", "?")
def ns(d): return d.get("metadata", {}).get("namespace")
def of(kind): return [d for d in docs if d.get("kind") == kind]

for cr in of("ClusterRole"):
    for rule in cr.get("rules") or []:
        hit = SENSITIVE & set(rule.get("resources") or [])
        if hit:
            bad.append(f"ClusterRole {name(cr)} grants {sorted(hit)} cluster-wide")
for crb in of("ClusterRoleBinding"):
    if name(crb) != ONLY_CRB:
        bad.append(f"ClusterRoleBinding {name(crb)} is not allowed (only {ONLY_CRB})")
for role, binding in ROLES.items():
    if not any(name(r) == role and ns(r) == NS for r in of("Role")):
        bad.append(f"Role {role} is missing in {NS}")
    rb = [b for b in of("RoleBinding") if name(b) == binding and ns(b) == NS]
    if not rb:
        bad.append(f"RoleBinding {binding} is missing in {NS}")
    elif rb[0].get("roleRef", {}).get("kind") != "Role" or rb[0].get("roleRef", {}).get("name") != role:
        bad.append(f"RoleBinding {binding} does not bind Role {role}")
for dep, cname in CONTAINERS.items():
    found = [d for d in of("Deployment") if name(d) == dep]
    if not found:
        bad.append(f"Deployment {dep} is missing")
        continue
    cs = [c for c in found[0]["spec"]["template"]["spec"].get("containers") or [] if c.get("name") == cname]
    if not cs:
        bad.append(f"Deployment {dep} has no container {cname}")
        continue
    env = [e for e in cs[0].get("env") or [] if e.get("name") == "OPERATOR_SCOPE_NAMESPACE"]
    if len(env) != 1 or env[0].get("value") != NS or "valueFrom" in env[0]:
        bad.append(f"container {cname} of {dep} lacks OPERATOR_SCOPE_NAMESPACE={NS}")

if bad:
    for b in bad:
        print(f"✗ operators scope: {b}", file=sys.stderr)
    sys.exit(1)
print("✓ operators scope: no cluster-wide secrets/pods rights; Roles in ryft-rabbitmq; both operators scoped to it")
PY

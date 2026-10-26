#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2034,SC2153,SC2129,SC2016
# Shared by scripts/admission_e2e.sh (T12-d): the checks of the admission policies (D120) against a
# kind cluster. Sourced; needs kind_guard.sh sourced and passed first. bash 3.2 compatible.
#
# Variables the entrypoint sets:
#   ROOT   this checkout: the chart, the committed values, the fixtures, the scripts
#   REPO   the tree under test: deploy/ and (for Argo CD's RBAC) infra/aws/kubernetes/{main,argocd}.tf
#   WORK   a temporary directory (results, renders, logs)
# Every cluster call goes through kc/hc (kind_guard.sh). Nothing here writes to REPO or ROOT.

K8S_DIR_REL=infra/aws/kubernetes
CHART_DIR="$ROOT/$K8S_DIR_REL/charts/admission-policy"
FIXTURE_DIR="$ROOT/$K8S_DIR_REL/admission-e2e/fixtures"
ARGO_CTRL=system:serviceaccount:argocd:argocd-application-controller
ARGO_SRV=system:serviceaccount:argocd:argocd-server
FIXTURE_GROUP=ryft-ci:fixtures

# ---------------------------------------------------------------------------------------------
# results

adm_init() {
  RESULTS="$WORK/results.tsv"
  : > "$RESULTS"
  mkdir -p "$WORK/log" "$WORK/render" "$WORK/crds"
  # Helm reads no repository of this machine and writes no cache outside WORK.
  export HELM_CONFIG_HOME="$WORK/helm/config" HELM_CACHE_HOME="$WORK/helm/cache" HELM_DATA_HOME="$WORK/helm/data"
  # Git over file:// only, as the repo's other jobs (kustomize remote bases are refused).
  export GIT_ALLOW_PROTOCOL=file
  FAILED=0
}

say() { printf '%s\n' "$*" >&2; }
group() { if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::group::$*"; else say "== $*"; fi; }
endgroup() { if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::endgroup::"; fi; return 0; }

# rec <id> <PASS|FAIL|SKIP|INFO> <detail>: one line of the summary.
rec() {
  local detail
  detail=$(printf '%s' "$3" | tr '\n\t' '  ')
  printf '%s\t%s\t%s\n' "$1" "$2" "$detail" >> "$RESULTS"
  say "[$2] $1: $detail"
  if [ "$2" = FAIL ]; then
    FAILED=1
    if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::error title=admission ${1}::${3}"; fi
  fi
  return 0
}

# adm_summary <file>: the table of rec lines, as Markdown.
adm_summary() {
  local out=$1
  {
    echo "## admission e2e"
    echo
    echo "| check | result | detail |"
    echo "| --- | --- | --- |"
    awk -F'\t' '{ gsub(/\|/, "\\|", $3); printf "| %s | %s | %s |\n", $1, $2, $3 }' "$RESULTS"
    echo
    echo "tree under test: \`${REPO}\`; mode: ${MODE:-ci}; Kubernetes $(kc version -o json 2>/dev/null | jq -r '.serverVersion.gitVersion // "?"')"
    if [ "${MODE:-ci}" = full ]; then
      echo
      echo "Stubbed: Vault and VSO (CRDs real; a loop marks VaultStaticSecrets synced and writes dummy Secrets), the service images (busybox under the exact names; migrate sleeps 15 s), the git remote (served in the cluster, the served Applications name it), Ingress addresses, the broker's size (1 replica, standard StorageClass, 2Gi). Real: the API server, the policies, cert-manager, Argo CD, the operators, the broker, the topology, the hooks' Jobs."
    fi
    echo
    echo "pass: $(awk -F'\t' '$2 == "PASS"' "$RESULTS" | wc -l | tr -d ' '), fail: $(awk -F'\t' '$2 == "FAIL"' "$RESULTS" | wc -l | tr -d ' '), skipped: $(awk -F'\t' '$2 == "SKIP"' "$RESULTS" | wc -l | tr -d ' ')"
  } > "$out"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat "$out" >> "$GITHUB_STEP_SUMMARY"; fi
}

# ---------------------------------------------------------------------------------------------
# facts of the tree

# chart_version <main.tf key>: the pinned chart version of infra/aws/kubernetes/main.tf.
chart_version() {
  sed -nE "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\{.*version[[:space:]]*=[[:space:]]*\"([^\"]+)\".*/\1/p" "$REPO/$K8S_DIR_REL/main.tf" | head -1
}
chart_repo() {
  sed -nE "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\{[[:space:]]*repository[[:space:]]*=[[:space:]]*\"([^\"]+)\".*/\1/p" "$REPO/$K8S_DIR_REL/main.tf" | head -1
}

# Does the tree carry T12-g (both operators namespace-scoped) and T12-k (the broker's 443 egress
# narrowed)? The chart refuses or flags the render until it does.
tree_has_scoped_operators() { [ -f "$REPO/deploy/rabbitmq/operators/cluster-operator/scoped-rbac.yaml" ] && [ -f "$REPO/deploy/rabbitmq/operators/topology-operator/scoped-rbac.yaml" ]; }
tree_broker_egress_public() {
  # an egress rule to 0.0.0.0/0 on 443 without `except`
  yq -e 'select(.kind == "NetworkPolicy") | .spec.egress[].to[] | select(.ipBlock.cidr == "0.0.0.0/0" and ((.ipBlock.except // []) | length == 0))' \
    "$REPO/deploy/rabbitmq/cluster/networkpolicy.yaml" >/dev/null 2>&1
}

# ---------------------------------------------------------------------------------------------
# cluster basics

adm_wait_crds() { kc wait --for=condition=Established crd --all --timeout=120s >/dev/null; }

# The CRDs of the add-ons whose objects the renders hold, at the versions infra/aws/kubernetes pins,
# and Argo CD's (the root render and the projects cannot be dry-run without them), plus the vendored
# RabbitMQ ones. CRDs only: no operator runs.
adm_install_crds() {
  local name repo ver extra file
  for spec in "vault_secrets_operator|vault-secrets-operator|" "cert_manager|cert-manager|--set crds.enabled=true" "traefik|traefik|" "argocd|argo-cd|--set crds.install=true"; do
    IFS='|' read -r key name extra <<EOF
$spec
EOF
    repo=$(chart_repo "$key"); ver=$(chart_version "$key")
    [ -n "$repo" ] && [ -n "$ver" ] || { rec "crds-$name" FAIL "no pinned chart for $key in main.tf"; continue; }
    file="$WORK/crds/$name.yaml"
    # shellcheck disable=SC2086
    if ! helm template x "$name" --repo "$repo" --version "$ver" --include-crds $extra 2>"$WORK/log/crds-$name.err" | yq 'select(.kind == "CustomResourceDefinition")' > "$file.all"; then
      rec "crds-$name" FAIL "helm template $name $ver failed: $(head -c 300 "$WORK/log/crds-$name.err")"; continue
    fi
    # Traefik's chart carries the Gateway API CRDs too; the renders use traefik.io only.
    if [ "$name" = traefik ]; then yq 'select(.spec.group == "traefik.io")' "$file.all" > "$file"; else cp "$file.all" "$file"; fi
    if kc apply --server-side --force-conflicts -f "$file" >"$WORK/log/crds-$name.out" 2>&1; then
      rec "crds-$name" PASS "$name $ver: $(yq '.metadata.name' "$file" | grep -vc '^---') CRDs"
    else
      rec "crds-$name" FAIL "$(tail -3 "$WORK/log/crds-$name.out")"
    fi
  done
  # RabbitMQ: the vendored release files, as the operators app applies them.
  kubectl kustomize "$REPO/deploy/rabbitmq/operators" | yq 'select(.kind == "CustomResourceDefinition")' > "$WORK/crds/rabbitmq.yaml"
  if kc apply --server-side --force-conflicts -f "$WORK/crds/rabbitmq.yaml" >"$WORK/log/crds-rabbitmq.out" 2>&1; then
    rec "crds-rabbitmq" PASS "$(yq '.metadata.name' "$WORK/crds/rabbitmq.yaml" | grep -vc '^---') CRDs from deploy/rabbitmq/operators"
  else
    rec "crds-rabbitmq" FAIL "$(tail -3 "$WORK/log/crds-rabbitmq.out")"
  fi
  adm_wait_crds || rec "crds-established" FAIL "a CRD did not become Established"
}

# The namespaces, labelled as infra/vault (kubernetes_namespace_v1.service), argocd.tf and the
# overlays create them: ryft-<service> and monitoring from the generated vault-namespaces
# directories, ryft-web and rabbitmq-system from the manifests Argo CD applies, argocd.
adm_create_namespaces() {
  local out="$WORK/namespaces.yaml" d svc ns level
  : > "$out"
  for d in "$REPO"/deploy/k8s/overlays/vault-namespaces/*/; do
    svc=$(basename "$d")
    ns=$(yq '.namespace' "$d/kustomization.yaml")
    level=restricted
    [ "$svc" != monitoring ] || level=privileged   # D51
    cat >> "$out" <<EOF
---
apiVersion: v1
kind: Namespace
metadata:
  name: $ns
  labels:
    app.kubernetes.io/part-of: ryft
    ryft.dev/service: $svc
    ryft.dev/vault-namespace: admin_ryft_$svc
    pod-security.kubernetes.io/enforce: $level
    pod-security.kubernetes.io/warn: $level
    pod-security.kubernetes.io/audit: $level
EOF
  done
  cat >> "$out" <<EOF
---
apiVersion: v1
kind: Namespace
metadata:
  name: argocd
  labels:
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/warn: restricted
    pod-security.kubernetes.io/audit: restricted
EOF
  { echo ---; cat "$REPO/deploy/k8s/services/web/namespace.yaml"; } >> "$out"
  { echo ---; kubectl kustomize "$REPO/deploy/rabbitmq/operators" | yq 'select(.kind == "Namespace")'; } >> "$out"
  # The service accounts infra/vault creates (a VaultAuth names each): the API server refuses a Pod
  # fixture for a service account that does not exist, before admission is asked.
  for d in "$REPO"/deploy/k8s/overlays/vault-namespaces/*/vault.yaml; do
    yq 'select(.kind == "VaultAuth") | "---\napiVersion: v1\nkind: ServiceAccount\nmetadata: { name: " + .spec.kubernetes.serviceAccount + ", namespace: " + .metadata.namespace + " }"' "$d" >> "$out"
  done
  if kc apply -f "$out" >"$WORK/log/namespaces.out" 2>&1; then
    rec "namespaces" PASS "$(kc get ns -l 'pod-security.kubernetes.io/enforce' -o name | wc -l | tr -d ' ') namespaces with Pod Security labels (ryft-*, ryft-web, rabbitmq-system, argocd, monitoring)"
  else
    rec "namespaces" FAIL "$(tail -3 "$WORK/log/namespaces.out")"
  fi
}

# ---------------------------------------------------------------------------------------------
# Argo CD's chart: the values of argocd.tf (evaluated by Terraform), the rendered RBAC

# adm_argo_values: $WORK/argocd-values.json from the tree's argocd.tf (local.argocd_values), or empty.
adm_argo_values() {
  ARGO_VALUES=
  if [ -n "${ARGO_VALUES_FILE:-}" ]; then ARGO_VALUES=$ARGO_VALUES_FILE; return 0; fi
  if ! command -v terraform >/dev/null 2>&1; then rec "argo-values" SKIP "terraform is not installed: Argo CD's chart defaults (wildcard RBAC) stand in for argocd.tf"; return 0; fi
  if "$ROOT/scripts/lib/tf_values.sh" "$REPO" argocd_values "$WORK/argocd-values.json" >"$WORK/log/tf_values.log" 2>&1; then
    ARGO_VALUES="$WORK/argocd-values.json"
    rec "argo-values" PASS "local.argocd_values of $K8S_DIR_REL/argocd.tf evaluated by terraform (mock providers)"
  else
    rec "argo-values" FAIL "terraform could not evaluate argocd.tf: $(tail -3 "$WORK/log/tf_values.log")"
  fi
}

# adm_argo_render: the argo-cd chart rendered as the helm_release does (release argocd, namespace
# argocd, the chart version of main.tf, the values above, CRDs included).
adm_argo_render() {
  local repo ver
  repo=$(chart_repo argocd); ver=$(chart_version argocd)
  ARGO_RENDER="$WORK/argocd-render.yaml"
  if [ -n "$ARGO_VALUES" ]; then
    helm template argocd argo-cd --repo "$repo" --version "$ver" --namespace argocd -f "$ARGO_VALUES" > "$ARGO_RENDER" 2>"$WORK/log/argo-render.err"
  else
    helm template argocd argo-cd --repo "$repo" --version "$ver" --namespace argocd > "$ARGO_RENDER" 2>"$WORK/log/argo-render.err"
  fi || { rec "argo-render" FAIL "helm template argo-cd $ver: $(head -c 300 "$WORK/log/argo-render.err")"; ARGO_RENDER=; return 1; }
  local what="the chart defaults"
  [ -z "$ARGO_VALUES" ] || what="the values of argocd.tf"
  rec "argo-render" PASS "argo-cd $ver with $what"
}

# Chart drift and what the narrowed roles must never hold (T12-f). With the chart's default roles
# (a tree without clusterRoleRules) the check says so and does not judge.
adm_check_argo_rbac() {
  [ -n "${ARGO_RENDER:-}" ] || { rec "argo-rbac" SKIP "no render"; return 0; }
  local roles="$WORK/argo-clusterroles.json" want="$WORK/argo-want.json" bad
  yq -o=json -I0 'select(.kind == "ClusterRole" and (.metadata.name == "argocd-application-controller" or .metadata.name == "argocd-server"))' "$ARGO_RENDER" | jq -s '.' > "$roles"
  if [ -z "$ARGO_VALUES" ] || [ "$(jq '.controller.clusterRoleRules.enabled // false' "$ARGO_VALUES")" != true ]; then
    NARROWED_RBAC=0
    rec "argo-rbac" SKIP "the tree's argocd.tf sets no clusterRoleRules: the controller holds the chart's wildcard role ($(jq -r '[.[] | select(.metadata.name == "argocd-application-controller") | .rules[] | select(.verbs | index("*"))] | length' "$roles") wildcard rules); the dry-runs below run as that identity (T12-f, #47, narrows it)"
    return 0
  fi
  NARROWED_RBAC=1
  # Drift: the rendered ClusterRole rules are exactly the rules of the values.
  for who in controller server; do
    local name=argocd-application-controller
    [ "$who" = controller ] || name=argocd-server
    jq -S '[.controller.clusterRoleRules.rules, .server.clusterRoleRules.rules][0:0] | .' "$ARGO_VALUES" >/dev/null
    jq -S --arg w "$who" '.[$w].clusterRoleRules.rules | map(. + {apiGroups: .apiGroups, resources: .resources, verbs: .verbs} | del(.resourceNames | select(. == null))) | sort_by(tojson)' "$ARGO_VALUES" > "$want"
    local got="$WORK/argo-got-$who.json"
    jq -S --arg n "$name" '.[] | select(.metadata.name == $n) | .rules | map(del(.resourceNames | select(. == null))) | sort_by(tojson)' "$roles" > "$got"
    if jq -e -n --slurpfile a "$want" --slurpfile b "$got" '$a[0] == $b[0]' >/dev/null; then
      rec "argo-rbac-drift-$who" PASS "the rendered ClusterRole $name carries exactly the $who rules of argocd.tf ($(jq 'length' "$got") rules)"
    else
      rec "argo-rbac-drift-$who" FAIL "the rendered ClusterRole $name differs from the $who rules of argocd.tf (the chart changed what clusterRoleRules renders?): $(diff "$want" "$got" | head -6 | tr '\n' ' ')"
    fi
  done
  # Invariants of the design (D120): no wildcard; nothing on secrets, pods/exec, pods/ephemeralcontainers,
  # serviceaccounts/token; no write on the admission policies and their bindings; the server writes only
  # events and applications.
  bad=$(jq -r '.[] | .metadata.name as $n | .rules[] | select(
      (.verbs | index("*")) or (.resources // [] | index("*")) or (.apiGroups // [] | index("*")) or (.nonResourceURLs // [] | length > 0)
      or ((.resources // []) | any(. == "secrets" or . == "pods/exec" or . == "pods/ephemeralcontainers" or . == "serviceaccounts/token" or . == "validatingadmissionpolicies" or . == "validatingadmissionpolicybindings" or . == "mutatingadmissionpolicies" or . == "mutatingadmissionpolicybindings"))
    ) | $n + ": " + (.resources // [] | join(","))' "$roles")
  if [ -z "$bad" ]; then rec "argo-rbac-invariants" PASS "no wildcard, no secrets, no pods/exec, no ephemeralcontainers, no serviceaccounts/token, nothing on the admission policies or bindings"; else rec "argo-rbac-invariants" FAIL "$bad"; fi
  bad=$(jq -r '.[] | select(.metadata.name == "argocd-server") | .rules[] | select((.verbs | any(. == "create" or . == "update" or . == "patch" or . == "delete" or . == "escalate" or . == "bind" or . == "impersonate")) and (((.resources // []) | all(. == "events" or . == "applications")) | not)) | (.resources // [] | join(","))' "$roles")
  if [ -z "$bad" ]; then rec "argo-rbac-server-readonly" PASS "argocd-server writes only events and applications"; else rec "argo-rbac-server-readonly" FAIL "argocd-server may write: $bad"; fi
}

# The RBAC objects that make the impersonated identities real: the service accounts, the two
# ClusterRoles and their bindings of the render (the narrowed ones when the tree has them, the
# chart's defaults otherwise), and a group that fixtures add to an identity so authorisation passes
# and the admission decision is what a fixture shows.
adm_install_argo_identities() {
  [ -n "${ARGO_RENDER:-}" ] || { rec "argo-identities" FAIL "no render of the argo-cd chart"; return 1; }
  yq 'select((.kind == "ServiceAccount" or .kind == "ClusterRole" or .kind == "ClusterRoleBinding" or .kind == "Role" or .kind == "RoleBinding") and (.metadata.name == "argocd-application-controller" or .metadata.name == "argocd-server"))' "$ARGO_RENDER" > "$WORK/argo-identities.yaml"
  cat >> "$WORK/argo-identities.yaml" <<EOF
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ryft-ci-fixtures
subjects:
  - { apiGroup: rbac.authorization.k8s.io, kind: Group, name: $FIXTURE_GROUP }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin }
EOF
  if kc apply --server-side --force-conflicts -f "$WORK/argo-identities.yaml" >"$WORK/log/argo-identities.out" 2>&1; then
    rec "argo-identities" PASS "service accounts, ClusterRoles and bindings of argocd-application-controller and argocd-server$([ "${NARROWED_RBAC:-0}" = 1 ] && echo ' (narrowed, T12-f)' || echo ' (chart defaults: wildcard)')"
  else
    rec "argo-identities" FAIL "$(tail -3 "$WORK/log/argo-identities.out")"
  fi
}

# ---------------------------------------------------------------------------------------------
# the chart

adm_projects_values() {
  yq eval-all '[select(di >= 0)] | {"projects": .}' "${PROJECTS_FILE:-$REPO/deploy/argocd/project.yaml}" > "$WORK/projects-values.yaml"
}

# adm_deny_values <file> <held>: every binding deny, brokerEgressPublic false (the chart's guards);
# <held> is a space-separated list of bindings left in audit (see adm_deny_held).
adm_deny_values() {
  local f=$1 held=$2 k
  {
    echo "actions:"
    for k in $(yq '.actions | keys | .[]' "$ROOT/$K8S_DIR_REL/admission-values.yaml" "$CHART_DIR/values.yaml" | sort -u); do
      case " $held " in *" $k "*) echo "  $k: audit" ;; *) echo "  $k: deny" ;; esac
    done
    echo "netpol:"
    echo "  brokerEgressPublic: false"
  } > "$f"
}

# adm_chart_install <audit|deny> [held bindings]: the committed values, P7's projects, and for deny
# the variant above. Waits until the API server has type-checked every policy.
adm_chart_install() {
  local variant=$1 held=${2:-} args=(-f "$ROOT/$K8S_DIR_REL/admission-values.yaml" -f "$WORK/projects-values.yaml")
  adm_projects_values
  if [ "$variant" = deny ]; then
    adm_deny_values "$WORK/deny-values.yaml" "$held"
    args+=(-f "$WORK/deny-values.yaml")
  fi
  if hc upgrade --install admission-policy "$CHART_DIR" --namespace kube-system "${args[@]}" ${CHART_EXTRA_ARGS[@]+"${CHART_EXTRA_ARGS[@]}"} >"$WORK/log/chart-$variant.out" 2>&1; then
    rec "chart-install-$variant" PASS "admission-policy $(yq '.version' "$CHART_DIR/Chart.yaml") installed in kube-system: the API server admitted $(kc get validatingadmissionpolicies -o name | wc -l | tr -d ' ') policies and $(kc get validatingadmissionpolicybindings -o name | wc -l | tr -d ' ') bindings (claim a: nothing guards the objects that guard)$([ -n "$held" ] && echo "; held in audit: $held")"
  else
    rec "chart-install-$variant" FAIL "helm: $(tail -5 "$WORK/log/chart-$variant.out")"
    return 1
  fi
  adm_wait_policies
}

# Every policy has been looked at by the type checker (status.observedGeneration == generation and
# status.typeChecking present); then status.typeChecking carries no warnings.
adm_wait_policies() {
  local i json bad pending
  for i in $(seq 1 60); do
    json=$(kc get validatingadmissionpolicies -o json)
    pending=$(printf '%s' "$json" | jq -r '[.items[] | select((.status.observedGeneration // 0) != .metadata.generation or (.status.typeChecking == null)) | .metadata.name] | join(" ")')
    [ -z "$pending" ] && break
    sleep 2
  done
  if [ -n "$pending" ]; then rec "typechecking" FAIL "the API server did not report type checking within 2 minutes for: $pending"; return 1; fi
  bad=$(printf '%s' "$json" | jq -r '.items[] | select((.status.typeChecking.expressionWarnings // []) | length > 0) | .metadata.name + ": " + ([.status.typeChecking.expressionWarnings[] | (.fieldRef // "") + " " + (.warning // "" | gsub("\n"; " ") | .[0:160])] | join(" | "))')
  if [ -z "$bad" ]; then
    rec "typechecking" PASS "status.typeChecking of all $(printf '%s' "$json" | jq '.items | length') policies: no warnings"
  else
    rec "typechecking" FAIL "$bad"
  fi
  sleep 3   # the bindings reach the admission plugin's informers
}

# ---------------------------------------------------------------------------------------------
# dry-runs of the renders

# adm_apps: "name<TAB>path" of every Application in deploy/argocd/apps.
adm_apps() {
  local f
  for f in "$REPO"/deploy/argocd/apps/*.yaml; do
    printf '%s\t%s\n' "$(yq '.metadata.name' "$f")" "$(yq '.spec.source.path' "$f")"
  done
}

# adm_dry_one <file> <identity|-> <errfile>: server-side dry-run apply of one object.
adm_dry_one() {
  local f=$1 who=$2 err=$3
  if [ "$who" = - ]; then
    kc apply --server-side --dry-run=server --force-conflicts --field-manager=argocd-controller -f "$f" >/dev/null 2>"$err"
  else
    kc apply --server-side --dry-run=server --force-conflicts --field-manager=argocd-controller --as "$who" -f "$f" >/dev/null 2>"$err"
  fi
}

# adm_prefix <label> <file>: the file's lines with the label as a first tab-separated column.
adm_prefix() { awk -v n="$1" 'BEGIN { FS = OFS = "\t" } { print n, $0 }' "$2"; }

# adm_dryrun_render <label> <rendered yaml> <identity|->: every object of a render, one by one,
# appended to $WORK/dry-<label>.tsv as "ok|refused|error<TAB>object<TAB>policy<TAB>message".
adm_dryrun_render() {
  local label=$1 render=$2 who=$3 dir="$WORK/render/$1" f n obj policy msg
  rm -rf "$dir"; mkdir -p "$dir"
  ( cd "$dir" && yq -s '"d" + ($index | tostring)' "$render" ) >/dev/null 2>&1
  : > "$WORK/dry-$label.tsv"
  for f in "$dir"/d*.yml; do
    [ -s "$f" ] || continue
    n=$(yq '.kind + " " + (.metadata.namespace // "-") + "/" + .metadata.name' "$f")
    [ "$n" != "null null/null" ] || continue
    if adm_dry_one "$f" "$who" "$f.err"; then
      # Warn actions come back as warnings on stderr even when the request passes.
      if grep -q "Validation failed for ValidatingAdmissionPolicy" "$f.err"; then
        policy=$(sed -nE "s/.*ValidatingAdmissionPolicy '([^']+)'.*/\1/p" "$f.err" | head -1)
        msg=$(sed -nE "s/.*with binding '[^']+': (.*)/\1/p" "$f.err" | head -1 | cut -c1-200)
        printf 'warned\t%s\t%s\t%s\n' "$n" "$policy" "$msg" >> "$WORK/dry-$label.tsv"
      else
        printf 'ok\t%s\t\t\n' "$n" >> "$WORK/dry-$label.tsv"
      fi
    elif grep -q "denied request" "$f.err"; then
      policy=$(sed -nE "s/.*ValidatingAdmissionPolicy '([^']+)'.*/\1/p" "$f.err" | head -1)
      msg=$(sed -nE "s/.*denied request: (.*)/\1/p" "$f.err" | head -1 | cut -c1-200)
      printf 'refused\t%s\t%s\t%s\n' "$n" "$policy" "$msg" >> "$WORK/dry-$label.tsv"
    else
      printf 'error\t%s\t\t%s\n' "$n" "$(tr '\n' ' ' < "$f.err" | cut -c1-240)" >> "$WORK/dry-$label.tsv"
    fi
  done
}

# adm_dryrun_apps <variant>: every Application's render as Argo CD's controller (RBAC included), and
# the project, the root and the applications themselves as the cluster admin (they are applied by
# hand or by Terraform, T12-b). Audit: nothing refused, nothing warned beyond the known flags. Deny:
# nothing refused beyond what the tree has not fixed yet (and then it must be refused: that is the
# policy working).
adm_dryrun_apps() {
  local variant=$1 name path total=0 refused=0 warned=0 errors=0 unexplained label
  group "dry-run of every app's render as Argo CD's application controller ($variant)"
  : > "$WORK/dry-all-$variant.tsv"
  while IFS="$(printf '\t')" read -r name path; do
    label="$variant-$name"
    kubectl kustomize "$REPO/$path" > "$WORK/render/$label.yaml" 2>"$WORK/log/kustomize-$name.err" || { rec "dryrun-$variant-$name" FAIL "kustomize $path: $(head -c 200 "$WORK/log/kustomize-$name.err")"; continue; }
    adm_dryrun_render "$label" "$WORK/render/$label.yaml" "$ARGO_CTRL"
    adm_prefix "$name" "$WORK/dry-$label.tsv" >> "$WORK/dry-all-$variant.tsv"
  done <<EOF
$(adm_apps)
EOF
  # The applications and the project/root, as the admin. They are not an app's render.
  awk 'FNR == 1 { print "---" } { print }' "$REPO/deploy/argocd/project.yaml" "$REPO/deploy/argocd/root.yaml" > "$WORK/render/$variant-argocd-hand.yaml"
  adm_dryrun_render "$variant-argocd-hand" "$WORK/render/$variant-argocd-hand.yaml" -
  adm_prefix argocd-hand "$WORK/dry-$variant-argocd-hand.tsv" >> "$WORK/dry-all-$variant.tsv"
  awk 'FNR == 1 { print "---" } { print }' "$REPO"/deploy/argocd/apps/*.yaml > "$WORK/render/$variant-argocd-apps.yaml"
  adm_dryrun_render "$variant-argocd-apps" "$WORK/render/$variant-argocd-apps.yaml" "$ARGO_CTRL"
  adm_prefix ryft-root "$WORK/dry-$variant-argocd-apps.tsv" >> "$WORK/dry-all-$variant.tsv"

  total=$(wc -l < "$WORK/dry-all-$variant.tsv" | tr -d ' ')
  refused=$(awk -F'\t' '$2 == "refused"' "$WORK/dry-all-$variant.tsv" | wc -l | tr -d ' ')
  warned=$(awk -F'\t' '$2 == "warned"' "$WORK/dry-all-$variant.tsv" | wc -l | tr -d ' ')
  errors=$(awk -F'\t' '$2 == "error"' "$WORK/dry-all-$variant.tsv" | wc -l | tr -d ' ')
  endgroup
  if [ "$errors" != 0 ]; then
    rec "dryrun-$variant-errors" FAIL "$errors objects failed for a reason that is no admission decision (RBAC, schema, missing CRD or namespace): $(awk -F'\t' '$2 == "error" { printf "%s %s: %s; ", $1, $3, $5 }' "$WORK/dry-all-$variant.tsv" | cut -c1-700)"
  fi
  # What the tree has not fixed yet is expected to be flagged, by the right policy, and only that.
  # P6 flags the operators' vendored cluster-wide roles until T12-g; P3 the broker's open 443 egress
  # until T12-k (and only when the variant asks for them: deny sets brokerEgressPublic false).
  unexplained=$(awk -F'\t' -v scoped="$(tree_has_scoped_operators && echo 1 || echo 0)" -v pub="$(tree_broker_egress_public && echo 1 || echo 0)" -v v="$variant" '
    ($2 == "refused" || $2 == "warned") {
      if ($1 == "rabbitmq-operators" && $4 == "ryft-operators-cluster" && scoped == 0) next
      if ($1 == "broker-rabbitmq" && $3 ~ /^NetworkPolicy ryft-rabbitmq\/rabbitmq$/ && $4 == "ryft-netpol" && pub == 1 && v == "deny") next
      printf "%s %s by %s; ", $1, $3, $4
    }' "$WORK/dry-all-$variant.tsv")
  if [ -n "$unexplained" ]; then
    rec "dryrun-$variant" FAIL "objects flagged that the tree does not explain: $unexplained"
  else
    rec "dryrun-$variant" PASS "$total objects of $(adm_apps | wc -l | tr -d ' ') apps, the root and the projects: $refused refused, $warned warned, all explained ($(tree_has_scoped_operators && echo 'T12-g present' || echo 'T12-g absent: P6 flags the vendored operator roles'); $(tree_broker_egress_public && echo 'T12-k absent: the broker egress is open' || echo 'T12-k present'))"
  fi
  if [ "$variant" = deny ]; then
    if ! tree_has_scoped_operators; then
      if awk -F'\t' '$1 == "rabbitmq-operators" && $2 == "refused" && $4 == "ryft-operators-cluster"' "$WORK/dry-all-deny.tsv" | grep -q .; then
        rec "dryrun-deny-p6-guards-operators" PASS "without T12-g, P6 in Deny refuses the vendored cluster-wide operator roles (the precondition in admission.tf is right)"
      else
        rec "dryrun-deny-p6-guards-operators" FAIL "without T12-g P6 should refuse the vendored operator ClusterRoles, and refused nothing"
      fi
    fi
  fi
}

# ---------------------------------------------------------------------------------------------
# fixtures

# adm_fixture_header <file> <key>: the first "# key: value" line.
adm_fixture_header() { sed -n "s/^# $2: //p" "$1" | head -1; }

# adm_run_fixture <file> <variant> -> sets FX_OUT, FX_RC. op: create (default), patch, delete, exec.
adm_run_fixture() {
  local f=$1 op who target patch ptype sub
  op=$(adm_fixture_header "$f" op); op=${op:-create}
  who=$(adm_fixture_header "$f" as)
  target=$(adm_fixture_header "$f" target)
  patch=$(adm_fixture_header "$f" patch)
  ptype=$(adm_fixture_header "$f" patch-type); ptype=${ptype:-merge}
  sub=$(adm_fixture_header "$f" subresource)
  local asargs=()
  [ -z "$who" ] || asargs=(--as "$who" --as-group "$FIXTURE_GROUP")
  FX_OUT="$WORK/log/fixture.out"
  case "$op" in
    create) kc create --dry-run=server -f "$f" -o name ${asargs[@]+"${asargs[@]}"} >"$FX_OUT" 2>&1 ;;
    patch)
      # shellcheck disable=SC2086
      kc patch $target --type "$ptype" -p "$patch" --dry-run=server ${sub:+--subresource "$sub"} ${asargs[@]+"${asargs[@]}"} >"$FX_OUT" 2>&1 ;;
    delete)
      # shellcheck disable=SC2086
      kc delete $target --dry-run=server ${asargs[@]+"${asargs[@]}"} >"$FX_OUT" 2>&1 ;;
    exec)
      # shellcheck disable=SC2086
      kc exec $target ${asargs[@]+"${asargs[@]}"} -- true >"$FX_OUT" 2>&1 ;;
    *) echo "unknown op $op in $f" > "$FX_OUT"; return 2 ;;
  esac
  FX_RC=$?
  return 0
}

# adm_fixtures <variant> <label> <dir>: every fixture of the directory. deny: a refused fixture must
# be refused by its policy with its message, an admitted one must pass; audit: nothing is refused, and
# a fixture that deny refuses warns with the same message.
adm_fixtures() {
  local variant=$1 label=$2 dir=$3 f n=0 pass=0 failed=0 refused_by msg expect norm name detail failures=
  [ -d "$dir" ] || { rec "fixtures-$label-$variant" FAIL "no fixture directory $dir"; return; }
  group "fixtures $label ($variant)"
  # A file may hold several fixtures, separated by a line "# ----"; a chunk without "# fixture:" is a preamble.
  local split="$WORK/fx/$label" src
  rm -rf "$split"; mkdir -p "$split"
  for src in "$dir"/*.yaml; do
    [ -f "$src" ] || continue
    awk -v out="$split" -v base="$(basename "$src" .yaml)" 'BEGIN { n = 0; f = sprintf("%s/%s-%02d.yaml", out, base, n) } /^# ----$/ { close(f); n++; f = sprintf("%s/%s-%02d.yaml", out, base, n); next } { print > f }' "$src"
  done
  for f in "$split"/*.yaml; do
    [ -f "$f" ] || continue
    grep -q '^# fixture: ' "$f" || continue
    n=$((n + 1))
    name=$(adm_fixture_header "$f" fixture)
    refused_by=$(adm_fixture_header "$f" refused-by)
    msg=$(adm_fixture_header "$f" message)
    expect=$(adm_fixture_header "$f" expect)
    adm_run_fixture "$f" "$variant"
    norm=$(tr -s ' \n\t' '   ' < "$FX_OUT")
    detail=
    if [ "$variant" = deny ] && [ -n "$refused_by" ]; then
      if [ "$FX_RC" != 0 ] && printf '%s' "$norm" | grep -qF "with binding '$refused_by' denied request: $msg"; then :; else
        detail="expected a refusal by $refused_by ($msg), got rc=$FX_RC: $(printf '%s' "$norm" | cut -c1-300)"
      fi
    elif [ "$variant" = audit ] && [ -n "$refused_by" ]; then
      # (a create of an object that exists answers AlreadyExists after admission: the warning is there)
      if { [ "$FX_RC" = 0 ] || printf '%s' "$norm" | grep -qF "AlreadyExists"; } && printf '%s' "$norm" | grep -qF "with binding '$refused_by': $msg"; then :; else
        detail="in Audit the request must pass with a warning from $refused_by ($msg), got rc=$FX_RC: $(printf '%s' "$norm" | cut -c1-300)"
      fi
    else
      # expect: admitted
      if [ "$FX_RC" = 0 ] && ! printf '%s' "$norm" | grep -qF "denied request"; then :; else
        detail="expected the request to pass, got rc=$FX_RC: $(printf '%s' "$norm" | cut -c1-300)"
      fi
    fi
    if [ -z "$detail" ]; then pass=$((pass + 1)); else failed=$((failed + 1)); failures="$failures [$(basename "$f"): $detail]"; say "  fixture differs: $(basename "$f"): $detail"; fi
  done
  endgroup
  if [ "$failed" = 0 ]; then
    rec "fixtures-$label-$variant" PASS "$pass/$n fixtures behave as written"
  else
    rec "fixtures-$label-$variant" FAIL "$failed of $n fixtures differ:$failures"
  fi
}

# ---------------------------------------------------------------------------------------------
# claims that need no workload

# audit annotations of the API server: the node container's audit log (kind runs the log on the node).
adm_audit_log() { "$ENGINE" exec "${KIND_CLUSTER}-control-plane" cat /var/log/kubernetes/audit.log 2>/dev/null; }

# Claim a: no admission policy runs on policy or binding objects. A policy written to match the
# policy and binding kinds themselves never fires: with its binding in Deny, creating another
# policy (and another binding) still passes.
adm_claim_a() {
  local cfgdir="$WORK/claim-a" out
  mkdir -p "$cfgdir"
  cat > "$cfgdir/a.yaml" <<'EOF'
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: zz-claim-a
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["admissionregistration.k8s.io"]
        apiVersions: ["*"]
        operations: ["CREATE", "UPDATE", "DELETE"]
        resources: ["validatingadmissionpolicies", "validatingadmissionpolicybindings"]
  validations:
    - expression: "false"
      message: "claim a: this policy must never fire"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: zz-claim-a
spec:
  policyName: zz-claim-a
  validationActions: ["Deny"]
EOF
  cat > "$cfgdir/b.yaml" <<'EOF'
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: zz-claim-a-probe
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["configmaps"]
  validations:
    - expression: "true"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: zz-claim-a-probe
spec:
  policyName: zz-claim-a-probe
  validationActions: ["Warn"]
EOF
  if ! out=$(kc apply -f "$cfgdir/a.yaml" 2>&1); then rec "claim-a" FAIL "the API server did not accept a policy that matches policy objects: $out"; return; fi
  sleep 5
  if out=$(kc apply -f "$cfgdir/b.yaml" 2>&1) && kc delete -f "$cfgdir/b.yaml" >/dev/null 2>&1; then
    rec "claim-a" PASS "a policy matching ValidatingAdmissionPolicy and -Binding (Deny, active) refused neither a further policy and binding nor their deletion: the objects that guard are exempt from admission, RBAC guards them"
  else
    rec "claim-a" FAIL "a policy written to match policy objects fired: $out"
  fi
  kc delete -f "$cfgdir/a.yaml" >/dev/null 2>&1 || true
}

# Claim b: webhook configurations are not exempt, so P6 sees them. In Deny the controller creating a
# webhook configuration that is none of the operators' is refused by P6 (and the carve-out names are not).
adm_claim_b() {
  local f="$FIXTURE_DIR/claims/claim-b-webhook.yaml" norm
  adm_run_fixture "$f" deny
  norm=$(tr -s ' \n\t' '   ' < "$FX_OUT")
  if [ "$FX_RC" != 0 ] && printf '%s' "$norm" | grep -qF "ValidatingAdmissionPolicy 'ryft-operators-cluster'"; then
    rec "claim-b" PASS "a ValidatingWebhookConfiguration written by Argo CD's controller was refused by P6: webhook configurations are not exempt from admission"
  else
    rec "claim-b" FAIL "P6 did not see a webhook configuration (rc=$FX_RC): $(printf '%s' "$norm" | cut -c1-300)"
  fi
}

# Claim c: CONNECT on pods/exec can be matched (P8). A pod outside ryft-rabbitmq, exec'd as the
# Cluster Operator's service account. Nothing runs in the pod: the request is refused before it
# reaches the kubelet; an admitted exec would run `true` in it.
adm_claim_c() {
  local ns=claim-c f="$FIXTURE_DIR/claims/claim-c-exec.yaml" norm
  kc create namespace "$ns" >/dev/null 2>&1 || true
  kc -n "$ns" apply -f - >/dev/null 2>&1 <<'EOF'
apiVersion: v1
kind: Pod
metadata: { name: probe }
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.10
EOF
  kc -n "$ns" wait --for=condition=Ready pod/probe --timeout=120s >/dev/null 2>&1 || true
  adm_run_fixture "$f" deny
  norm=$(tr -s ' \n\t' '   ' < "$FX_OUT")
  if [ "$FX_RC" != 0 ] && printf '%s' "$norm" | grep -qF "ValidatingAdmissionPolicy 'ryft-operator-reach'" && printf '%s' "$norm" | grep -qF "denied request"; then
    rec "claim-c" PASS "pods/exec by the Cluster Operator's service account in a namespace other than ryft-rabbitmq was refused by P8 on CONNECT"
  else
    rec "claim-c" FAIL "P8 did not refuse the exec (rc=$FX_RC): $(printf '%s' "$norm" | cut -c1-300)"
  fi
  kc delete namespace "$ns" --wait=false >/dev/null 2>&1 || true
}

# Claim d: dry-run requests carry the validation_failure audit annotation, and a real one too. In
# Audit a violating object is dry-run created and created for real (a ConfigMap in ryft-api with
# API_KEY, P5); the API server's audit log must hold the annotation on both, the dry-run one with
# dryRun=All in the request URI.
adm_claim_d() {
  local cm="zz-claim-d" tmp="$WORK/claim-d.yaml" log dry real
  cat > "$tmp" <<EOF
apiVersion: v1
kind: ConfigMap
metadata: { name: $cm, namespace: ryft-api }
data: { API_KEY: not-a-secret-just-a-claim-d-probe }
EOF
  kc create --dry-run=server -f "$tmp" >/dev/null 2>&1
  kc create -f "$tmp" >/dev/null 2>&1
  kc -n ryft-api delete configmap "$cm" >/dev/null 2>&1 || true
  sleep 3
  log=$(adm_audit_log) || log=
  if [ -z "$log" ]; then rec "claim-d" SKIP "the API server's audit log is not readable from the node container"; return; fi
  dry=$(printf '%s\n' "$log" | jq -c --arg cm "$cm" 'select(.verb == "create" and .objectRef.resource == "configmaps" and .objectRef.name == $cm and (.requestURI | test("dryRun=All")) and (.annotations | has("validation.policy.admission.k8s.io/validation_failure")))' 2>/dev/null | head -1)
  real=$(printf '%s\n' "$log" | jq -c --arg cm "$cm" 'select(.verb == "create" and .objectRef.resource == "configmaps" and .objectRef.name == $cm and ((.requestURI | test("dryRun")) | not) and (.annotations | has("validation.policy.admission.k8s.io/validation_failure")))' 2>/dev/null | head -1)
  if [ -n "$dry" ] && [ -n "$real" ]; then
    rec "claim-d" PASS "the audit log holds validation.policy.admission.k8s.io/validation_failure on the dry-run create (requestURI with dryRun=All) and on the real one, so CloudWatch's query can tell them apart"
  else
    rec "claim-d" FAIL "annotation on the dry-run request: ${dry:+yes}${dry:-no}; on the real request: ${real:+yes}${real:-no}"
  fi
}

# Claim h, the NEGATIVE test of T12-f: the application controller's escalate on a few named
# ClusterRoles plus bind on them is escalation-equivalent. Without the policies it succeeds: the
# controller rewrites rabbitmq-cluster-operator-role to carry write on the admission policies and
# binds itself to it, after which it may write them. With P6 in Deny both steps are refused (the role
# carries what no operator role may; the binding names a subject that is not an operator service
# account). Run as the real service account (no fixture group): its RBAC is what is tested.
# Needs the narrowed role (escalate/bind by resourceNames); with the chart default (wildcard) the
# first phase succeeds trivially and the check says so.
adm_claim_h() {
  local phase=$1   # open | guarded
  local tmp="$WORK/claim-h" out1 out2 can
  mkdir -p "$tmp"
  cat > "$tmp/role.yaml" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: rabbitmq-cluster-operator-role }
rules:
  - apiGroups: ["admissionregistration.k8s.io"]
    resources: ["validatingadmissionpolicies", "validatingadmissionpolicybindings"]
    verbs: ["create", "update", "patch", "delete"]
EOF
  cat > "$tmp/binding.yaml" <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: rabbitmq-cluster-operator-role }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: rabbitmq-cluster-operator-role }
subjects:
  - { kind: ServiceAccount, name: argocd-application-controller, namespace: argocd }
EOF
  # The operators' role as it is in the cluster (read-only here, made by the admin: P6 judges Argo
  # CD's writers only), then the controller's two steps.
  cat > "$tmp/benign.yaml" <<'EOF2'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: rabbitmq-cluster-operator-role }
rules:
  - apiGroups: ["rabbitmq.com"]
    resources: ["rabbitmqclusters"]
    verbs: ["get", "list", "watch"]
EOF2
  kc delete -f "$tmp/binding.yaml" --ignore-not-found >/dev/null 2>&1
  kc delete -f "$tmp/role.yaml" --ignore-not-found >/dev/null 2>&1
  kc apply -f "$tmp/benign.yaml" >/dev/null 2>&1
  out1=$(kc apply -f "$tmp/role.yaml" --as "$ARGO_CTRL" 2>&1); local rc1=$?
  out2=$(kc apply -f "$tmp/binding.yaml" --as "$ARGO_CTRL" 2>&1); local rc2=$?
  can=$(kc auth can-i create validatingadmissionpolicybindings --as "$ARGO_CTRL" 2>/dev/null | head -1)
  # clean up whatever was created, as the admin
  kc delete -f "$tmp/binding.yaml" --ignore-not-found >/dev/null 2>&1
  kc delete -f "$tmp/role.yaml" --ignore-not-found >/dev/null 2>&1
  if [ "$phase" = open ]; then
    if [ "$rc1" = 0 ] && [ "$rc2" = 0 ] && [ "$can" = yes ]; then
      rec "claim-h-open" PASS "without the policies the controller rewrote rabbitmq-cluster-operator-role (escalate), bound itself to it (bind) and could then write ValidatingAdmissionPolicyBindings: the hole exists in $([ "${NARROWED_RBAC:-0}" = 1 ] && echo "T12-f's narrowed RBAC (stated in #47)" || echo "the chart-default RBAC")"
    elif [ "${NARROWED_RBAC:-0}" = 1 ] && [ "$rc1" != 0 ]; then
      rec "claim-h-open" FAIL "with T12-f's RBAC and no policies the controller could not rewrite the operator role (rc=$rc1: $(printf '%s' "$out1" | tr '\n' ' ' | cut -c1-200)): either escalate by resourceNames does not do what #47 assumes, or the hole is closed already"
    else
      rec "claim-h-open" FAIL "unexpected: role rc=$rc1, binding rc=$rc2, can-i=$can: $(printf '%s %s' "$out1" "$out2" | tr '\n' ' ' | cut -c1-300)"
    fi
  else
    local n1 n2
    n1=$(printf '%s' "$out1" | tr -s ' \n' '  ')
    n2=$(printf '%s' "$out2" | tr -s ' \n' '  ')
    if [ "$rc1" != 0 ] && printf '%s' "$n1" | grep -qF "ValidatingAdmissionPolicy 'ryft-operators-cluster'" \
      && [ "$rc2" != 0 ] && printf '%s' "$n2" | grep -qF "ValidatingAdmissionPolicy 'ryft-operators-cluster'" \
      && { [ "${NARROWED_RBAC:-0}" != 1 ] || [ "$can" != yes ]; }; then
      if [ "${NARROWED_RBAC:-0}" = 1 ]; then
        rec "claim-h-guarded" PASS "with P6 in Deny the role rewrite and the self-binding are both refused (ryft-operators-cluster); the controller cannot write ValidatingAdmissionPolicyBindings (can-i: $can)"
      else
        rec "claim-h-guarded" PASS "with P6 in Deny the role rewrite and the self-binding are both refused (ryft-operators-cluster); this tree has the chart-default RBAC, which T12-f narrows"
      fi
    else
      rec "claim-h-guarded" FAIL "with P6 in Deny: role rc=$rc1 ($(printf '%s' "$n1" | cut -c1-200)), binding rc=$rc2, can-i=$can"
    fi
  fi
}

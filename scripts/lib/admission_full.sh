#!/usr/bin/env bash
# shellcheck shell=bash disable=SC2034,SC2153,SC2015,SC1007
# The real stack of the kind end-to-end (scripts/admission_e2e.sh --mode full), after the CI part:
# cert-manager and the internal CA, Argo CD from argocd.tf's values (T12-f's RBAC when the tree has
# it), the tree served to Argo CD from inside the cluster, the RabbitMQ operators and the broker synced
# by Argo CD, the policies first in Audit and then in Deny, with a manual sync of every app, the hooks,
# terminate-op, the broker restart of D98, the operators' reach, claims i and m.
#
# What is stubbed (and nothing else):
#   - Vault and the Vault Secrets Operator: the VSO CRDs are there, a loop in this script marks every
#     VaultStaticSecret SecretSynced and writes the Secret it names with dummy keys (the amqp-user-*
#     ones with a username and a password). P4 judges the real objects; no secret is real.
#   - The service images: ghcr.io/timkrebs/ryft/<name>:<the overlay's tag> are busybox images built here
#     and loaded into kind under exactly those names (the policies judge the names): an HTTP server on
#     8080 for the services and the site, a 15 second sleep for migrate (the PreSync hook and, with the
#     argument smoke, the PostSync hook). The real images need a database. The Job specs are the real
#     ones; what they run is not.
#   - The tree is served to Argo CD from inside the cluster (a dumb-HTTP git repository in gitsrv)
#     under the URL it names (git's url.<>.insteadOf in the repo-server): the Applications are the
#     committed ones.
#   - Ingress status (no load balancer in kind) is patched so Argo CD's health check passes.
#   - The broker runs one replica on the default StorageClass and 2Gi (the tree's three replicas on
#     gp3 do not fit a runner): a copy of rabbitmqcluster.yaml in the served repository is patched.
# Not stubbed: the API server, the policies, cert-manager, Argo CD (every component), the operators,
# the broker, the topology, the hooks' Jobs, the NetworkPolicies as objects.

ARGO_PASS=
BG_PIDS=()

adm_bg() { "$@" & BG_PIDS+=("$!"); }

adm_full_kill_bg() {
  local p
  for p in ${BG_PIDS[@]+"${BG_PIDS[@]}"}; do kill "$p" 2>/dev/null; done
  return 0
}

# adm_poll <seconds> <command...>: run the command every 5 seconds until it succeeds.
adm_poll() {
  local deadline=$((SECONDS + $1)); shift
  while ! "$@" >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$deadline" ] || return 1
    sleep 5
  done
}

# ---------------------------------------------------------------------------------------------
# the cluster's base: cert-manager, the internal CA

adm_full_reset() {
  hc uninstall admission-policy --namespace kube-system >/dev/null 2>&1
  adm_unbaseline
  kc delete -f "$WORK/argo-identities.yaml" --ignore-not-found >/dev/null 2>&1
  rec "full-reset" INFO "the CI part's policies, baseline and stand-in Argo CD identities removed"
}

adm_full_certmanager() {
  local repo ver
  repo=$(chart_repo cert_manager); ver=$(chart_version cert_manager)
  if hc upgrade --install cert-manager cert-manager --repo "$repo" --version "$ver" --namespace cert-manager --create-namespace \
      --set crds.enabled=false --set resources.requests.cpu=10m --set cainjector.resources.requests.cpu=10m --set webhook.resources.requests.cpu=10m \
      --wait --timeout 6m >"$WORK/log/cert-manager.out" 2>&1; then
    helm template cluster-issuers "$ROOT/$K8S_DIR_REL/charts/cluster-issuers" --namespace cert-manager --set email=ci@example.com \
      --set region=eu-central-1 --set hostedZoneID=Z0 --set dnsZone=example.test --show-only templates/internal-ca.yaml > "$WORK/internal-ca.yaml" 2>"$WORK/log/internal-ca.err"
    if adm_poll 120 kc apply -f "$WORK/internal-ca.yaml" && adm_poll 120 kc wait --for=condition=Ready clusterissuer/ryft-internal --timeout=5s; then
      rec "full-cert-manager" PASS "cert-manager $ver and the internal CA (ClusterIssuer ryft-internal, from the cluster-issuers chart) are ready"
    else
      rec "full-cert-manager" FAIL "the internal CA did not become ready: $(kc get clusterissuer ryft-internal -o jsonpath='{.status.conditions[*].message}' 2>&1 | cut -c1-300)"
    fi
  else
    rec "full-cert-manager" FAIL "helm: $(tail -4 "$WORK/log/cert-manager.out")"
  fi
}

# ---------------------------------------------------------------------------------------------
# stubs: VSO, ingress status, the images, the served repository

# A background loop: every VaultStaticSecret is SecretSynced and its Secret exists; every Ingress has an address.
# Everything it reads comes from objects of the tree under test, so every name and label is checked
# before it reaches a command line (a label key such as --kubeconfig must not become a flag), and the
# labels are passed as separate quoted arguments. What it refuses is listed in $WORK/stub-refused.txt,
# which adm_full turns into a failure.
adm_full_stub_loop() {
  local list ns name dest gen labels k v
  while :; do
    list=$(kc get vaultstaticsecrets -A -o json 2>/dev/null | jq -r '.items[] | [.metadata.namespace, .metadata.name, .spec.destination.name, (.metadata.generation | tostring), ((.spec.destination.labels // {}) | tojson)] | @tsv')
    while IFS="$(printf '\t')" read -r ns name dest gen labels; do
      [ -n "$name" ] || continue
      if ! adm_valid_name "$ns" || ! adm_valid_name "$name" || ! [[ $dest =~ ^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$ ]] || ! [[ $gen =~ ^[0-9]{1,18}$ ]]; then
        echo "VaultStaticSecret $ns/$name: odd namespace, name, destination or generation" >> "$WORK/stub-refused.txt"
        continue
      fi
      if ! kc -n "$ns" get secret "$dest" >/dev/null 2>&1; then
        case "$dest" in
          amqp-user-*) kc -n "$ns" create secret generic "$dest" --from-literal=username="${dest#amqp-user-}" --from-literal=password="stub-$(date +%s)" >/dev/null 2>&1 ;;
          *) kc -n "$ns" create secret generic "$dest" --from-literal=STUB=1 >/dev/null 2>&1 ;;
        esac
      fi
      # The labels the VaultStaticSecret puts on its Secret (the Topology Operator wants one): one
      # quoted argument per label, each key and value valid as Kubernetes validates a label.
      while IFS="$(printf '\t')" read -r k v; do
        [ -n "$k" ] || continue
        if adm_valid_label_key "$k" && adm_valid_label_value "$v"; then
          kc -n "$ns" label secret "$dest" "$k=$v" --overwrite >/dev/null 2>&1
        else
          echo "VaultStaticSecret $ns/$name: the label '$(printf '%s' "$k" | cut -c1-60)' is not a valid label" >> "$WORK/stub-refused.txt"
        fi
      done <<EOF
$(printf '%s' "$labels" | jq -r 'to_entries[] | [.key, (.value | tostring)] | @tsv' 2>/dev/null)
EOF
      kc -n "$ns" patch vaultstaticsecret "$name" --type merge --subresource status \
        -p "{\"status\":{\"lastGeneration\":$gen,\"conditions\":[{\"type\":\"SecretSynced\",\"status\":\"True\",\"reason\":\"Stub\",\"message\":\"stubbed by admission_e2e.sh\",\"observedGeneration\":$gen,\"lastTransitionTime\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}]}}" >/dev/null 2>&1
    done <<EOF
$list
EOF
    for ns in $(kc get ingress -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}' 2>/dev/null); do
      adm_valid_name "${ns%%/*}" && adm_valid_name "${ns##*/}" || continue
      kc -n "${ns%%/*}" patch ingress "${ns##*/}" --type merge --subresource status -p '{"status":{"loadBalancer":{"ingress":[{"ip":"10.0.0.1"}]}}}' >/dev/null 2>&1
    done
    sleep 4
  done
}

# The service images the overlay pins (ghcr.io/timkrebs/ryft/<name>:<7 hex>) are stub images: built
# here as localhost/ryft-stub/<name>:<tag> and loaded into kind, then tagged INSIDE the node under the
# exact names the policies judge. No ghcr.io/timkrebs/ryft/... tag ever exists in the local image store
# (an image a developer pulled for real is never replaced), and the stub images are removed from it
# after the load.
adm_full_images() {
  local dir="$WORK/stub-images" imgs ref n=0 local_ref node="${KIND_CLUSTER}-control-plane" built=()
  mkdir -p "$dir"
  printf 'FROM docker.io/library/busybox:1.37\nRUN mkdir -p /www && echo ok > /www/healthz && echo ok > /www/readyz\nEXPOSE 8080\nENTRYPOINT ["httpd", "-f", "-p", "8080", "-h", "/www"]\n' > "$dir/server.containerfile"
  printf 'FROM docker.io/library/busybox:1.37\nENTRYPOINT ["/bin/sh", "-c", "sleep 15"]\n' > "$dir/job.containerfile"
  imgs=$(kubectl kustomize "$REPO/deploy/k8s/overlays/eks" | sed -nE 's/^[[:space:]]+(- )?image: (ghcr\.io\/timkrebs\/ryft\/[^[:space:]]+)$/\2/p' | sort -u)
  [ -n "$imgs" ] || { rec "full-images" FAIL "the EKS overlay names no ghcr.io/timkrebs/ryft image"; return 1; }
  while IFS= read -r ref; do
    if ! [[ $ref =~ ^ghcr\.io/timkrebs/ryft/[a-z0-9-]+:[0-9a-f]{7}$ ]]; then
      rec "full-images" FAIL "the overlay names the image '$ref', which is not ghcr.io/timkrebs/ryft/<name>:<7 hex>"; return 1
    fi
    local_ref="localhost/ryft-stub/${ref#ghcr.io/timkrebs/ryft/}"
    case "$ref" in
      */migrate:*) "$ENGINE" build -q -t "$local_ref" -f "$dir/job.containerfile" "$dir" >/dev/null ;;
      *) "$ENGINE" build -q -t "$local_ref" -f "$dir/server.containerfile" "$dir" >/dev/null ;;
    esac || { rec "full-images" FAIL "$ENGINE build of $local_ref failed"; return 1; }
    built+=("$local_ref")
    n=$((n + 1))
  done <<< "$imgs"
  "$ENGINE" save -o "$dir/images.tar" "${built[@]}" >/dev/null 2>&1 || { rec "full-images" FAIL "$ENGINE save failed"; return 1; }
  if ! kind load image-archive "$dir/images.tar" --name "$KIND_CLUSTER" >"$WORK/log/kind-load.out" 2>&1; then
    rec "full-images" FAIL "kind load: $(tail -3 "$WORK/log/kind-load.out")"
    "$ENGINE" rmi -f "${built[@]}" >/dev/null 2>&1
    return 1
  fi
  while IFS= read -r ref; do
    local_ref="localhost/ryft-stub/${ref#ghcr.io/timkrebs/ryft/}"
    "$ENGINE" exec "$node" ctr --namespace k8s.io images tag "$local_ref" "$ref" >>"$WORK/log/kind-load.out" 2>&1 \
      || { rec "full-images" FAIL "ctr images tag $local_ref $ref in the node: $(tail -2 "$WORK/log/kind-load.out")"; "$ENGINE" rmi -f "${built[@]}" >/dev/null 2>&1; return 1; }
  done <<< "$imgs"
  "$ENGINE" rmi -f "${built[@]}" >/dev/null 2>&1
  rec "full-images" PASS "$n stub images (busybox) built as localhost/ryft-stub/..., loaded into kind and tagged in the node as $(printf '%s' "$imgs" | tr '\n' ' '); none of those names exists in the local $ENGINE store"
}

# adm_full_git_publish: the staged tree (WORK/git/work) committed and made available to the cluster.
adm_full_git_publish() {
  local g="$WORK/git" msg=${1:-"admission e2e"}
  git -C "$g/work" -c user.name=admission-e2e -c user.email=admission-e2e@example.invalid add -A >/dev/null
  git -C "$g/work" -c user.name=admission-e2e -c user.email=admission-e2e@example.invalid commit -q --allow-empty -m "$msg" >/dev/null
  git -C "$g/work" push -q origin main 2>/dev/null || git -C "$g/work" push -q origin HEAD:main
  kc -n gitsrv cp --no-preserve=true "$g/repo.git" gitsrv/git:/srv/ -c git >/dev/null 2>&1
}

# The URL Argo CD reads the tree from. The Applications and the project name it instead of GitHub's
# (rewritten in the served copy), and P7's constant follows (argocd.repoURL of the chart, projects
# from the served project.yaml): the policy compares with whatever repository the chart is given.
GIT_URL=http://git.gitsrv.svc.cluster.local:8080/repo.git
GIT_REAL_URL=https://github.com/timkrebs/ryft-platform.git

adm_full_gitserver() {
  local g="$WORK/git" arch
  rm -rf "$g"; mkdir -p "$g/work" "$g/build"
  # REPO is the copy of the tracked files made at the start (admission_e2e.sh, tracked_copy.sh): a
  # git-ignored deploy/k8s/secrets.yaml of the tree under test is not in it, so it cannot be served.
  cp -R "$REPO/deploy" "$g/work/deploy"
  # The tree is served from here: its own Applications point at this server.
  grep -rl "$GIT_REAL_URL" "$g/work/deploy/argocd" | while read -r f; do sed -i.bak "s|$GIT_REAL_URL|$GIT_URL|g" "$f" && rm -f "$f.bak"; done
  # The broker for a runner (see the header): one replica, the default StorageClass, small.
  yq -i '.spec.replicas = 1 | .spec.persistence.storageClassName = "standard" | .spec.persistence.storage = "2Gi" | .spec.resources.requests.cpu = "100m"' "$g/work/deploy/rabbitmq/cluster/rabbitmqcluster.yaml"
  PROJECTS_FILE="$g/work/deploy/argocd/project.yaml"
  CHART_EXTRA_ARGS=(--set "argocd.repoURL=$GIT_URL")
  git init -q --bare -b main "$g/repo.git"
  git -C "$g/work" init -q -b main
  git -C "$g/work" remote add origin "$g/repo.git"
  # The server: git http-backend behind net/http (gitsrv.go), in an Alpine image with git.
  arch=$(kc get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
  if ! ( cd "$g/build" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o gitsrv "$ROOT/scripts/lib/gitsrv.go" ) >"$WORK/log/gitsrv-build.out" 2>&1; then
    rec "full-gitserver" FAIL "go build of scripts/lib/gitsrv.go: $(tail -3 "$WORK/log/gitsrv-build.out")"; return 1
  fi
  printf 'FROM docker.io/library/alpine:3.22\nRUN apk add --no-cache git git-daemon\nCOPY gitsrv /gitsrv\nUSER 65532:65532\nENTRYPOINT ["/gitsrv"]\n' > "$g/build/Containerfile"
  if ! "$ENGINE" build -q -t localhost/gitsrv:e2e -f "$g/build/Containerfile" "$g/build" >"$WORK/log/gitsrv-image.out" 2>&1 \
    || ! "$ENGINE" save -o "$g/build/gitsrv.tar" localhost/gitsrv:e2e >/dev/null 2>&1 \
    || ! kind load image-archive "$g/build/gitsrv.tar" --name "$KIND_CLUSTER" >>"$WORK/log/gitsrv-image.out" 2>&1; then
    rec "full-gitserver" FAIL "the gitsrv image: $(tail -3 "$WORK/log/gitsrv-image.out")"; return 1
  fi
  "$ENGINE" rmi -f localhost/gitsrv:e2e >/dev/null 2>&1   # it lives in the node now
  kc create namespace gitsrv >/dev/null 2>&1
  kc apply -f - >/dev/null 2>&1 <<'EOF'
apiVersion: v1
kind: Pod
metadata: { name: git, namespace: gitsrv, labels: { app: git } }
spec:
  containers:
    - name: git
      image: localhost/gitsrv:e2e
      imagePullPolicy: Never
      ports: [{ containerPort: 8080 }]
      readinessProbe: { httpGet: { path: /healthz, port: 8080 } }
      volumeMounts: [{ name: srv, mountPath: /srv }]
  volumes: [{ name: srv, emptyDir: {} }]
---
apiVersion: v1
kind: Service
metadata: { name: git, namespace: gitsrv }
spec:
  selector: { app: git }
  ports: [{ port: 8080, targetPort: 8080 }]
EOF
  if ! kc -n gitsrv wait --for=condition=Ready pod/git --timeout=180s >/dev/null 2>&1; then rec "full-gitserver" FAIL "the git pod did not start"; return 1; fi
  adm_full_git_publish "the tree under test"
  if kc -n gitsrv exec git -- git ls-remote /srv/repo.git 2>/dev/null | grep -q refs/heads/main; then
    rec "full-gitserver" PASS "$(git -C "$g/work" rev-parse --short HEAD): deploy/ of the tree served in the cluster (git smart HTTP, gitsrv.go) as $GIT_URL, which the served Applications, the project and P7's constant name instead of GitHub's URL; stage patch: the broker runs 1 replica, StorageClass standard, 2Gi"
  else
    rec "full-gitserver" FAIL "the repository did not reach the pod"
  fi
}

# ---------------------------------------------------------------------------------------------
# Argo CD

adm_full_argocd() {
  local repo ver
  if [ -z "${ARGO_VALUES:-}" ]; then rec "full-argocd" FAIL "no values of argocd.tf (terraform is needed in the full mode)"; return 1; fi
  repo=$(chart_repo argocd); ver=$(chart_version argocd)
  cat > "$WORK/argocd-e2e-values.yaml" <<EOF
crds:
  install: false
server:
  ingress:
    enabled: false
EOF
  if hc upgrade --install argocd argo-cd --repo "$repo" --version "$ver" --namespace argocd -f "$ARGO_VALUES" -f "$WORK/argocd-e2e-values.yaml" --wait --timeout 10m >"$WORK/log/argocd.out" 2>&1; then
    rec "full-argocd" PASS "argo-cd $ver installed with argocd.tf's values$([ "${NARROWED_RBAC:-0}" = 1 ] && echo " (T12-f's narrowed ClusterRoles, respectRBAC strict, ApplicationSet at 0)" || echo " (this tree: the chart's default ClusterRoles)"); stand-ins: no Ingress, the admin password"
  else
    rec "full-argocd" FAIL "helm: $(tail -6 "$WORK/log/argocd.out" | cut -c1-300)"
    return 1
  fi
}

# The argocd CLI runs inside the argocd-server pod (the server's own version, no port-forward, nothing
# installed here) and talks to the real server on localhost:8080 as the admin account: every sync,
# terminate-op and refresh below is argocd-server's own write.
ac() { kc -n argocd exec deploy/argocd-server -- argocd --config /tmp/argocd.config "$@"; }

adm_full_argocd_login() {
  local hash
  ARGO_PASS="admission-e2e-$(date +%s)"
  hash=$(kc -n argocd exec deploy/argocd-server -- argocd account bcrypt --password "$ARGO_PASS" 2>/dev/null)
  [ -n "$hash" ] || return 1
  kc -n argocd patch secret argocd-secret --type merge -p "{\"stringData\":{\"admin.password\":\"$hash\",\"admin.passwordMtime\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}}" >/dev/null || return 1
  adm_poll 90 kc -n argocd exec deploy/argocd-server -- argocd login localhost:8080 --config /tmp/argocd.config --username admin --password "$ARGO_PASS" --plaintext
}

# "name sync health phase" of every Application.
adm_full_apps_state() {
  kc -n argocd get applications -o json 2>/dev/null | jq -r '.items[] | [.metadata.name, (.status.sync.status // "-"), (.status.health.status // "-"), (.status.operationState.phase // "-"), (if .operation then "op" else "" end)] | @tsv' | sort
}

adm_full_apps_ready() {
  local json
  json=$(kc -n argocd get applications -o json 2>/dev/null) || return 1
  [ "$(printf '%s' "$json" | jq '.items | length')" -ge "$(($(adm_apps | wc -l) + 1))" ] || return 1
  printf '%s' "$json" | jq -e 'all(.items[]; .status.sync.status == "Synced" and .status.health.status == "Healthy" and (.operation == null) and (.status.operationState.phase == "Succeeded"))' >/dev/null
}

# adm_full_wait_apps <label> <seconds>
adm_full_wait_apps() {
  local label=$1 deadline=$((SECONDS + $2)) last=0 app kicks=""
  while ! adm_full_apps_ready; do
    # An Application whose sync ended in Error/Failed is synced once more (at most 3 times each), and
    # said so: automated sync does not retry the same commit after its own retries are spent.
    for app in $(kc -n argocd get applications -o json 2>/dev/null | jq -r '.items[] | select((.status.operationState.phase == "Error" or .status.operationState.phase == "Failed") and .operation == null) | .metadata.name'); do
      adm_valid_name "$app" || continue
      [ "$(printf '%s' "$kicks" | grep -o "$app " | wc -l)" -lt 3 ] || continue
      kicks="$kicks$app "
      rec "full-sync-retry" INFO "$app: the sync ended $(kc -n argocd get application "$app" -o jsonpath='{.status.operationState.phase}'): $(kc -n argocd get application "$app" -o jsonpath='{.status.operationState.message}' | cut -c1-260); synced again"
      ac app sync "$app" --async >/dev/null 2>&1
    done
    if [ "$SECONDS" -ge "$deadline" ]; then
      rec "$label" FAIL "not every Application is Synced, Healthy and Succeeded within $2 s:$(adm_full_apps_state | awk '{ printf " [%s %s %s %s]", $1, $2, $3, $4 }') ; failing operations: $(kc -n argocd get applications -o json | jq -r '[.items[] | select(.status.operationState.phase != null and .status.operationState.phase != "Succeeded") | .metadata.name + ": " + (.status.operationState.message // "" | .[0:300])] | join(" | ")')"
      return 1
    fi
    if [ $((SECONDS - last)) -ge 40 ]; then last=$SECONDS; say "  apps:$(adm_full_apps_state | awk '{ printf " %s=%s/%s/%s", $1, $2, $3, $4 }')"; fi
    sleep 5
  done
  rec "$label" PASS "$(adm_full_apps_state | wc -l | tr -d ' ') Applications Synced, Healthy, last operation Succeeded"
}

# Which hooks ran in the last sync of ryft-platform, and how did they end?
adm_full_check_hooks() {
  local label=$1 json hooks
  json=$(kc -n argocd get application ryft-platform -o json)
  hooks=$(printf '%s' "$json" | jq -r '[.status.operationState.syncResult.resources[]? | select(.hookType != null) | .hookType + " " + .kind + "/" + .name + " " + (.hookPhase // "?")] | join("; ")')
  if printf '%s' "$hooks" | grep -q "PreSync Job/migrate Succeeded" && printf '%s' "$hooks" | grep -q "PostSync Job/smoke Succeeded"; then
    rec "$label" PASS "ryft-platform's last sync ran PreSync Job/migrate and PostSync Job/smoke to Succeeded (stub images, the real Job specs)"
  else
    rec "$label" FAIL "hooks of the last sync of ryft-platform: ${hooks:-none}; phase $(printf '%s' "$json" | jq -r '.status.operationState.phase') $(printf '%s' "$json" | jq -r '.status.operationState.message // "" | .[0:300]')"
  fi
}

# ---------------------------------------------------------------------------------------------
# findings of the audit log

# adm_full_explained <audit|deny> <held>: the validation failures a stage is expected to log, one shape
# per line: "<policy/binding:actions regex>|<requester regex>|<resource regex>|<message regex>". Only
# what the tree has not fixed yet, only with the exact requester, resource and message the harness pins:
#   - until T12-g, P6 flags the two vendored cluster-wide operator ClusterRoles that grant Secrets;
#   - until T12-k, in the deny stage, P3 flags the broker's open 443 egress (held in Audit).
# A tree that has T12-g / T12-k explains nothing of that kind.
adm_full_explained() {
  local stage=$1 held=${2:-}
  if ! tree_has_scoped_operators; then
    # (every failing validation is in the annotation, not only the first: the three P6 messages the two
    # vendored roles can carry)
    printf '%s\t%s\t%s\t%s\n' '^ryft-operators-cluster/[^:]+:(Warn\+Audit|Audit|Warn)$' '^system:serviceaccount:argocd:argocd-application-controller$' '^clusterroles$' '^P6: a (Role or )?ClusterRole of the operators (must not (hold a wildcard verb|grant Secrets)|is read-only)'
  fi
  if [ "$stage" = deny ] && [ -n "$held" ] && tree_broker_egress_public; then
    printf '%s\t%s\t%s\t%s\n' '^ryft-netpol/[^:]+:(Warn\+Audit|Audit|Warn)$' '^system:serviceaccount:argocd:argocd-application-controller$' '^networkpolicies$' '^P3: an egress ipBlock must be 0.0.0.0/0 with the private ranges excepted on 443/5432'
  fi
}

# adm_full_findings <label> <since-timestamp> [explained shapes]: the validation failures the API server
# logged since then (the audit log, grepped on the node), by policy/binding, requester, resource, dry-run
# and message. A line that matches none of the explained shapes (adm_full_explained) is a finding.
adm_full_findings() {
  local label=$1 since=$2 explained=${3:-} log all unexplained="" line rest cnt v u r d m shape explained_n=0 rc=0 s_v s_u s_r s_m
  log=$(adm_audit_grep validation_failure) || rc=$?
  if [ "$rc" != 0 ]; then adm_audit_unreadable "$label" "no log on the node of ${KIND_CLUSTER}"; return; fi
  all=$(printf '%s\n' "$log" | jq -r --arg since "$since" 'select(.requestReceivedTimestamp >= $since and (.annotations // {} | has("validation.policy.admission.k8s.io/validation_failure"))) | (.annotations["validation.policy.admission.k8s.io/validation_failure"] | fromjson | .[] | select(.policy != null)) as $f | [($f.policy + "/" + $f.binding + ":" + ($f.validationActions | join("+"))), .user.username, (.objectRef.resource // "-"), ((.requestURI | test("dryRun")) | tostring), ($f.message // "" | gsub("[\\t\\n]"; " ") | .[0:200])] | @tsv' 2>/dev/null | sort | uniq -c | sort -rn | sed -E 's/^ *([0-9]+) /\1\t/')
  if [ -z "$all" ]; then rec "$label" PASS "no validation failure in the audit log since the stage began"; return; fi
  while IFS="$(printf '\t')" read -r cnt v u r d m; do
    [ -n "$cnt" ] || continue
    line=0
    while IFS= read -r shape; do
      [ -n "$shape" ] || continue
      IFS="$(printf '\t')" read -r s_v s_u s_r s_m <<EOF
$shape
EOF
      if [[ $v =~ $s_v ]] && [[ $u =~ $s_u ]] && [[ $r =~ $s_r ]] && [[ $m =~ $s_m ]]; then line=1; break; fi
    done <<EOF
$explained
EOF
    if [ "$line" = 1 ]; then explained_n=$((explained_n + cnt)); else unexplained="$unexplained [$cnt x $v by $u on $r dryrun=$d: $(printf '%s' "$m" | cut -c1-90)]"; fi
  done <<EOF
$all
EOF
  if [ -z "$unexplained" ]; then
    rec "$label" PASS "$explained_n validation failures logged since the stage began, every one a known shape of what the tree has not fixed yet"
  else
    rec "$label" FAIL "unexplained validation failures:$(printf '%s' "$unexplained" | cut -c1-900)"
  fi
}

adm_now() { date -u +%Y-%m-%dT%H:%M:%S.000000Z; }

# ---------------------------------------------------------------------------------------------
# the operators, the broker, the topology

adm_full_check_broker() {
  local label=$1 json ready cond
  json=$(kc -n ryft-rabbitmq get rabbitmqcluster rabbitmq -o json 2>/dev/null)
  cond=$(printf '%s' "$json" | jq -r '[.status.conditions[]? | select(.type == "AllReplicasReady" or .type == "ReconcileSuccess") | .type + "=" + .status] | join(" ")')
  ready=$(printf "%s" "$cond" | grep -o "True" | wc -l | tr -d " ")
  if [ "$ready" = 2 ]; then
    rec "$label" PASS "RabbitmqCluster rabbitmq: $cond"
  else
    rec "$label" FAIL "RabbitmqCluster rabbitmq: ${cond:-no status}"
  fi
}

adm_full_check_topology() {
  local label=$1 bad total kinds="vhosts exchanges queues bindings policies users permissions topicpermissions"
  total=0; bad=
  for k in $kinds; do
    n=$(kc get "$k.rabbitmq.com" -A -o json 2>/dev/null | jq -r '[.items[] | select((.status.conditions // []) | map(select(.type == "Ready" and .status == "True")) | length == 0) | .kind + "/" + .metadata.name] | join(",")')
    c=$(kc get "$k.rabbitmq.com" -A -o name 2>/dev/null | wc -l | tr -d ' ')
    total=$((total + c))
    [ -z "$n" ] || bad="$bad $n"
  done
  if [ "$total" -gt 0 ] && [ -z "$bad" ]; then
    rec "$label" PASS "the Topology Operator made $total topology objects Ready (vhosts, exchanges, queues, bindings, policies, users, permissions)"
  else
    rec "$label" FAIL "topology objects: $total, not Ready:${bad:- none listed}"
  fi
}

# No "forbidden" in the operators' logs: the scoped Roles carry every verb they need. The logs are
# read into files first and the exit status of kubectl is checked (a failed read is no "nothing found").
adm_full_check_operator_logs() {
  local label=$1 since=$2 n1 n2 sample dep
  for dep in rabbitmq-cluster-operator messaging-topology-operator; do
    if ! kc -n rabbitmq-system logs "deploy/$dep" --since-time="$since" > "$WORK/log/operator-$dep.log" 2>"$WORK/log/operator-$dep.err"; then
      rec "$label" FAIL "kubectl logs deploy/$dep failed: $(head -c 300 "$WORK/log/operator-$dep.err")"
      return
    fi
  done
  n1=$(grep -ci forbidden "$WORK/log/operator-rabbitmq-cluster-operator.log" || true)
  n2=$(grep -ci forbidden "$WORK/log/operator-messaging-topology-operator.log" || true)
  if [ "$n1" = 0 ] && [ "$n2" = 0 ]; then
    rec "$label" PASS "no 'forbidden' in the logs of the Cluster Operator and the Topology Operator since the stage began$(tree_has_scoped_operators && echo ' (namespace-scoped, T12-g)' || echo ' (cluster-wide roles: this tree has no T12-g)')"
  else
    sample=$(grep -ih forbidden "$WORK"/log/operator-*.log | head -2 | cut -c1-400)
    rec "$label" FAIL "'forbidden' in the operators' logs (cluster operator $n1, topology operator $n2): $sample"
  fi
}

# adm_can_i <user> "<verb resource [-n ns] ...>": the first line of `kubectl auth can-i` as that user. The
# words are constants of this file, split on blanks into an array (no globbing).
adm_can_i() {
  local who=$1 words=()
  read -r -a words <<< "$2"
  kca "$who" auth can-i "${words[@]}" 2>/dev/null | head -1
}

# kubectl auth can-i for the operators' service accounts. With T12-g (both scoped) the negatives are no;
# without it the cluster-wide reads are what the PR removes, reported as such.
adm_full_check_can_i() {
  local label=$1 sa who fails="" wrong="" q ans
  for sa in rabbitmq-cluster-operator messaging-topology-operator; do
    who="system:serviceaccount:rabbitmq-system:$sa"
    for q in "list secrets -A" "get secrets -n ryft-api" "get secrets -n argocd" "create pods/exec -n ryft-api" "update pods -n ryft-api" "create secrets -n ryft-api" "create clusterroles" "update validatingadmissionpolicybindings" "create validatingwebhookconfigurations"; do
      ans=$(adm_can_i "$who" "$q")
      [ "$ans" = no ] || fails="$fails [$sa: $q = ${ans:-?}]"
    done
    # and what they need, in the broker's namespace
    q="get secrets -n ryft-rabbitmq"
    ans=$(adm_can_i "$who" "$q")
    [ "$ans" = yes ] || wrong="$wrong [$sa: $q = ${ans:-?}]"
    if [ "$sa" = rabbitmq-cluster-operator ]; then
      q="update statefulsets.apps -n ryft-rabbitmq"
      ans=$(adm_can_i "$who" "$q")
      [ "$ans" = yes ] || wrong="$wrong [$sa: $q = ${ans:-?}]"
    fi
  done
  if [ -z "$fails" ] && [ -z "$wrong" ]; then
    rec "$label" PASS "both operator service accounts: no to secrets outside ryft-rabbitmq, pods/exec and pod updates elsewhere, ClusterRoles, admission objects; yes to what they need in ryft-rabbitmq"
  elif tree_has_scoped_operators; then
    rec "$label" FAIL "can-i: should be no:${fails:- none}; should be yes:${wrong:- none}"
  else
    rec "$label" INFO "this tree has no T12-g: the operators are cluster-wide, so these answers are yes (what #51 removes):${fails:- none}${wrong:+; missing:$wrong}"
  fi
}

adm_cond_broker_ready() { kc -n ryft-rabbitmq get rabbitmqcluster rabbitmq -o json | jq -e '[.status.conditions[]? | select(.type == "AllReplicasReady" and .status == "True")] | length == 1' >/dev/null; }
adm_cond_platform_presync() { kc -n argocd get application ryft-platform -o json | jq -e '.status.operationState.phase == "Running" and ([.status.operationState.syncResult.resources[]? | select(.hookType == "PreSync")] | length > 0)' >/dev/null; }
adm_cond_platform_ended() { kc -n argocd get application ryft-platform -o json | jq -e '.status.operationState.phase != "Running" and .status.operationState.phase != "Terminating" and .operation == null' >/dev/null; }

# The positive control of claim c: the Cluster Operator's pods/exec into the broker (what its upgrade
# checks do) passes P8's CONNECT rule in Deny; the same request in any other namespace is refused (claim c).
adm_full_check_operator_exec() {
  local label=$1 out
  if out=$(kca system:serviceaccount:rabbitmq-system:rabbitmq-cluster-operator -n ryft-rabbitmq exec pod/rabbitmq-server-0 -c rabbitmq -- true 2>&1); then
    rec "$label" PASS "the Cluster Operator's service account may exec into the broker's pod (P8 admits pods/exec in ryft-rabbitmq)"
  else
    rec "$label" FAIL "pods/exec by the Cluster Operator into the broker: $(printf '%s' "$out" | cut -c1-300)"
  fi
}

# D98: the fallback restart of the broker, as the cluster admin, with the policies in Deny.
adm_full_broker_restart() {
  local label=$1 uid0 uid1 out
  uid0=$(kc -n ryft-rabbitmq get pod rabbitmq-server-0 -o jsonpath='{.metadata.uid}' 2>/dev/null)
  out=$(kc -n ryft-rabbitmq rollout restart statefulset rabbitmq-server 2>&1) || { rec "$label" FAIL "rollout restart: $(printf '%s' "$out" | cut -c1-300)"; return; }
  if kc -n ryft-rabbitmq rollout status statefulset rabbitmq-server --timeout=420s >"$WORK/log/restart.out" 2>&1; then
    uid1=$(kc -n ryft-rabbitmq get pod rabbitmq-server-0 -o jsonpath='{.metadata.uid}' 2>/dev/null)
    if [ -n "$uid0" ] && [ "$uid0" != "$uid1" ]; then
      adm_poll 180 adm_cond_broker_ready
      rec "$label" PASS "kubectl -n ryft-rabbitmq rollout restart statefulset rabbitmq-server: the update (only restartedAt) and the new pod (P2's exemptions: the StatefulSet's owner and creator) were admitted; the pod was replaced and the cluster is ready again"
    else
      rec "$label" FAIL "the rollout finished but the pod was not replaced"
    fi
  else
    rec "$label" FAIL "rollout status: $(tail -3 "$WORK/log/restart.out" | cut -c1-300); StatefulSet events: $(kc -n ryft-rabbitmq get events --field-selector reason=FailedCreate -o jsonpath='{.items[*].message}' 2>/dev/null | cut -c1-400)"
  fi
}

# ---------------------------------------------------------------------------------------------
# Argo CD operations

# terminate-op: a sync of ryft-platform is in PreSync (the stub migrate sleeps) and is terminated.
adm_full_terminate_op() {
  local label=$1 phase rev i synced
  ac app sync ryft-platform --async >"$WORK/log/sync-async.out" 2>&1 || { rec "$label" FAIL "argocd app sync --async: $(tail -2 "$WORK/log/sync-async.out")"; return; }
  # claim k: what argocd-server wrote as the revision of a plain sync
  rev=
  for i in $(seq 1 20); do
    rev=$(kc -n argocd get application ryft-platform -o json | jq -r 'if .operation.sync then (.operation.sync.revision // "<absent>") else empty end')
    [ -z "$rev" ] || break
    sleep 1
  done
  adm_poll 90 adm_cond_platform_presync || true
  if ac app terminate-op ryft-platform >"$WORK/log/terminate.out" 2>&1; then
    adm_poll 120 adm_cond_platform_ended || true
    phase=$(kc -n argocd get application ryft-platform -o jsonpath='{.status.operationState.phase}')
    if [ "$phase" = Failed ] || [ "$phase" = Error ]; then
      rec "$label" PASS "argocd app sync ryft-platform --async, then argocd app terminate-op: the operation ended ($phase: $(kc -n argocd get application ryft-platform -o jsonpath='{.status.operationState.message}' | cut -c1-80)); P10 did not match it, P7 let the status writes through"
    else
      rec "$label" FAIL "terminate-op was accepted but the operation is $phase"
    fi
  else
    rec "$label" FAIL "argocd app terminate-op: $(tail -3 "$WORK/log/terminate.out" | cut -c1-300) (phase now: $(kc -n argocd get application ryft-platform -o jsonpath='{.status.operationState.phase}'))"
  fi
  # Claim k, answered (D122): argocd-server writes the FULL commit SHA of the revision the Application was
  # compared with, not "" and not main; P10 lets it through because that SHA is the synced revision.
  synced=$(kc -n argocd get application ryft-platform -o jsonpath='{.status.sync.revision}' 2>/dev/null)
  if [[ $rev =~ ^[0-9a-f]{40}$ ]] && [ "$rev" = "$synced" ]; then
    rec "claim-k" PASS "argocd-server wrote operation.sync.revision = the full commit SHA ${rev:0:12}... (the revision ryft-platform is synced to), not main: P10's third alternative (the synced revision) is what admits a manual sync, and 'sync --revision <other sha>' is refused"
  else
    rec "claim-k" INFO "what argocd-server wrote as operation.sync.revision for a plain 'argocd app sync --async': '${rev:-not seen}' (synced revision '${synced:-?}'; P10 allows \"\", main and the synced revision); D122 says it is the full SHA, which this run did not see"
  fi
}

# A manual sync of every app through argocd-server (the path P10 judges), in wave order.
adm_full_manual_syncs() {
  local label=$1 app failed=
  for app in $(kc -n argocd get applications -o json | jq -r '.items | sort_by(.metadata.annotations["argocd.argoproj.io/sync-wave"] // "0" | tonumber) | .[].metadata.name'); do
    adm_valid_name "$app" || { failed="$failed [an Application named '$(printf '%s' "$app" | cut -c1-40)' is no DNS name]"; continue; }
    if ! ac app sync "$app" --timeout 300 >"$WORK/log/sync-$app.out" 2>&1; then
      failed="$failed [$app: $(grep -iE 'denied|forbidden|error|failed' "$WORK/log/sync-$app.out" | head -2 | tr '\n' ' ' | cut -c1-300)]"
    fi
  done
  if [ -z "$failed" ]; then
    rec "$label" PASS "argocd app sync of every Application through argocd-server succeeded"
  else
    rec "$label" FAIL "manual syncs failed:$failed"
  fi
}

# Claim i: resource.respectRBAC strict with no cluster-wide Secrets for the controller.
adm_full_claim_i() {
  local since=$1 setting can errs
  setting=$(kc -n argocd get configmap argocd-cm -o jsonpath='{.data.resource\.respectRBAC}' 2>/dev/null)
  can=$(kca "$ARGO_CTRL" auth can-i list secrets -A 2>/dev/null | head -1)
  if [ "$setting" != strict ]; then
    rec "claim-i" SKIP "argocd-cm has no resource.respectRBAC: strict in this tree (T12-f)"
    return
  fi
  # The controller's log since the run began, read into a file and checked for a failed read.
  if ! kc -n argocd logs statefulset/argocd-application-controller --since-time="$since" > "$WORK/log/argocd-controller.log" 2>"$WORK/log/argocd-controller.err"; then
    rec "claim-i" FAIL "kubectl logs statefulset/argocd-application-controller failed: $(head -c 300 "$WORK/log/argocd-controller.err")"
    return
  fi
  errs=$(grep -ciE 'forbidden|cannot (list|watch) resource' "$WORK/log/argocd-controller.log" || true)
  if [ "$can" = no ] && [ "$errs" = 0 ]; then
    rec "claim-i" PASS "with resource.respectRBAC: strict the controller (cannot list Secrets cluster-wide: can-i $can) kept every Application's tree and health current through all syncs; no 'forbidden' or 'cannot list' in its log since the run began"
  else
    rec "claim-i" FAIL "respectRBAC strict: can-i list secrets -A = $can, $errs forbidden-like lines in the controller log: $(grep -iE 'forbidden|cannot (list|watch) resource' "$WORK/log/argocd-controller.log" | head -2 | cut -c1-300)"
  fi
}

# Claim m: kube-state-metrics custom-resource-state can export ValidatingAdmissionPolicyBinding (name,
# policy, the intended action label, validationActions), given rbac.extraRules for list/watch.
adm_full_claim_m() {
  local out
  cat > "$WORK/ksm-values.yaml" <<'EOF'
rbac:
  extraRules:
    - apiGroups: ["admissionregistration.k8s.io"]
      resources: ["validatingadmissionpolicybindings"]
      verbs: ["list", "watch"]
customResourceState:
  enabled: true
  config:
    kind: CustomResourceStateMetrics
    spec:
      resources:
        - groupVersionKind: { group: admissionregistration.k8s.io, version: v1, kind: ValidatingAdmissionPolicyBinding }
          metricNamePrefix: ryft_admission
          labelsFromPath: { binding: [metadata, name], policy: [spec, policyName] }
          metrics:
            - name: binding_action
              help: "The validation actions of an admission policy binding"
              each:
                type: StateSet
                stateSet:
                  labelName: action
                  path: [spec, validationActions]
                  list: [Deny, Warn, Audit]
              labelsFromPath:
                intended_action: [metadata, labels, admission.ryft.dev/intended-action]
selfMonitor: { enabled: false }
resources: { requests: { cpu: 10m, memory: 32Mi } }
EOF
  kc create namespace ksm >/dev/null 2>&1
  if ! hc upgrade --install ksm kube-state-metrics --repo https://prometheus-community.github.io/helm-charts --version 8.6.0 --namespace ksm -f "$WORK/ksm-values.yaml" --set 'collectors={namespaces}' --wait --timeout 5m >"$WORK/log/ksm.out" 2>&1; then
    rec "claim-m" FAIL "helm install of kube-state-metrics: $(tail -3 "$WORK/log/ksm.out" | cut -c1-300)"
    return
  fi
  out=$(kc get --raw "/api/v1/namespaces/ksm/services/ksm-kube-state-metrics:8080/proxy/metrics" 2>&1 | grep '^ryft_admission_binding_action' )
  if printf '%s' "$out" | grep -q 'action="Deny"' && printf '%s' "$out" | grep -q 'policy="ryft-vso"'; then
    rec "claim-m" PASS "kube-state-metrics custom-resource-state exports the bindings ($(printf '%s\n' "$out" | grep -c 'binding=')  series, e.g. $(printf '%s\n' "$out" | grep 'binding="ryft-vso"' | tr '\n' ' ' | cut -c1-260))"
  elif kc -n ksm logs deploy/ksm-kube-state-metrics 2>&1 | grep -q "no matching CRD found for configured GVK"; then
    # Not a failure of this run: the claim of the design is wrong. Recorded as INFO so the run stays usable
    # as a gate; it turns into a PASS the day kube-state-metrics can export built-in kinds.
    rec "claim-m" INFO "DISPROVED with kube-state-metrics 8.6.0 (v2.20.0): custom-resource-state only exports CustomResourceDefinitions (\"no matching CRD found for configured GVK admissionregistration.k8s.io/v1 ValidatingAdmissionPolicyBinding\"), so rbac.extraRules cannot make it export the bindings. T12-e needs another source (k8s-manifest-tail with the bindings in its object list and a log alert, or an exporter of its own)"
  else
    rec "claim-m" FAIL "no ryft_admission_binding_action series for the bindings: $(printf '%s' "$out" | head -3 | cut -c1-300) $(kc -n ksm logs deploy/ksm-kube-state-metrics 2>&1 | tail -3 | cut -c1-300)"
  fi
}

# ---------------------------------------------------------------------------------------------

adm_full() {
  local start held="" run_start
  run_start=$(adm_now)
  : > "$WORK/stub-refused.txt"
  group "full: the stack"
  adm_full_reset
  adm_full_certmanager
  adm_full_images
  adm_full_gitserver
  adm_full_argocd || { endgroup; return; }
  adm_full_argocd_login || rec "full-argocd-login" FAIL "argocd login (inside the argocd-server pod) failed"
  adm_bg adm_full_stub_loop
  endgroup

  # Which bindings the tree is ready to have in Deny: P6 refuses the operators' cluster-wide roles
  # until T12-g, P3 the broker's open egress until T12-k.
  tree_has_scoped_operators || held="$held operators-cluster"
  tree_broker_egress_public && held="$held netpol"
  held=${held# }

  group "full: Audit, the first sync of every app"
  start=$(adm_now)
  adm_chart_install audit
  kc apply -f "$WORK/git/work/deploy/argocd/project.yaml" -f "$WORK/git/work/deploy/argocd/root.yaml" >"$WORK/log/bootstrap.out" 2>&1 \
    && rec "full-bootstrap" PASS "project.yaml and root.yaml applied by the admin (Terraform's job, T12-b); Argo CD takes it from here" \
    || rec "full-bootstrap" FAIL "$(tail -3 "$WORK/log/bootstrap.out")"
  adm_full_wait_apps "full-sync-audit" 1500
  adm_full_check_hooks "full-hooks-audit"
  adm_full_check_broker "full-broker-audit"
  adm_full_check_topology "full-topology-audit"
  adm_full_check_operator_logs "full-operator-logs-audit" "$start"
  adm_full_findings "full-findings-audit" "$start" "$(adm_full_explained audit "$held")"
  endgroup

  group "full: Deny${held:+ (held in Audit until the tree has T12-g / T12-k: $held)}"
  start=$(adm_now)
  adm_chart_install deny "$held"
  adm_full_manual_syncs "full-sync-deny"
  adm_full_wait_apps "full-apps-deny" 900
  adm_full_check_hooks "full-hooks-deny"
  adm_full_findings "full-findings-deny" "$start" "$(adm_full_explained deny "$held")"
  adm_full_broker_restart "full-broker-restart"
  adm_full_check_broker "full-broker-deny"
  adm_full_terminate_op "full-terminate-op"
  adm_full_wait_apps "full-apps-after-terminate" 600
  adm_full_check_operator_logs "full-operator-logs-deny" "$start"
  adm_full_check_can_i "full-operators-can-i"
  adm_full_check_operator_exec "full-operator-exec"
  adm_full_claim_i "$run_start"
  adm_full_claim_m
  endgroup
  # What the stand-in for VSO refused to act on (a value of the tree that is no valid name or label).
  if [ -s "$WORK/stub-refused.txt" ]; then
    rec "full-stub-refused" FAIL "the VSO stand-in refused values of the tree under test: $(sort -u "$WORK/stub-refused.txt" | head -3 | tr '\n' ';' | cut -c1-400)"
  fi
}

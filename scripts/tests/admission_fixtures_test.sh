#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2329,SC2034
# The kind run's fixture machinery for P12 (scripts/lib/admission_lib.sh) against fakes of kubectl, in
# `make test-scripts`; no cluster is contacted:
#   - adm_fixture_pairs and the fixtures' headers: a request that several bindings refuse lists one
#     refused-by/message pair per binding; every pair in the committed fixtures is a refusal the CEL
#     harness pins (testdata/messages.golden), so a typo or a reworded message cannot pass unnoticed;
#   - adm_fixtures: Deny passes when ONE of the pairs is the denial, Audit when EVERY pair warns;
#   - adm_argo_scope_secretkey (the first server.secretkey, then its change and removal);
#   - adm_dryrun_p12, adm_check_argo_scope_policy, adm_full_p12_findings (claim n), adm_full_claim_o and
#     adm_full_argo_restart (claim p) on canned answers.
# (no -e: the library is written for `set -uo pipefail`, its functions run commands that fail on purpose,
# and every check below is an explicit comparison)
set -uo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ admission_fixtures_test: $*" >&2; exit 1; }
command -v jq >/dev/null || { echo "✓ admission_fixtures_test (skipped: no jq)"; exit 0; }

ROOT=$HERE WORK=$tmp REPO=$tmp/tree MODE=full KIND_CLUSTER=test
export ROOT WORK REPO MODE
. "$HERE/scripts/lib/admission_lib.sh"
. "$HERE/scripts/lib/admission_full.sh"
mkdir -p "$WORK/log"
adm_init   # RESULTS, FAILED
say() { :; }
TAB=$(printf '\t')

# result <id> -> PASS|FAIL|SKIP|INFO of the last rec with that id
result() { awk -F'\t' -v id="$1" '$1 == id { r = $2 } END { print r }' "$RESULTS"; }
detail() { awk -F'\t' -v id="$1" '$1 == id { r = $3 } END { print r }' "$RESULTS"; }
expect() { [ "$(result "$1")" = "$2" ] || fail "$3: $1 expected $2, got '$(result "$1")' ($(detail "$1" | cut -c1-300))"; }

# ---- the headers -------------------------------------------------------------------------------
fx=$tmp/fx; mkdir -p "$fx"
cat > "$fx/f.yaml" <<'EOF'
# preamble, not a fixture
# ----
# fixture: single
# op: patch
# as: u1
# target: configmap x -n y
# patch: {}
# refused-by: ryft-a
# message: A: msg a
# ----
# fixture: multi
# op: create
# as: u2
# refused-by: ryft-b
# message: B: msg b with 'quotes' and a: colon
# refused-by: ryft-c
# message: C: msg c
apiVersion: v1
kind: ConfigMap
metadata: { name: x }
# ----
# fixture: open
# op: delete
# target: job x
# expect: admitted
EOF
split="$tmp/split"; mkdir -p "$split"
awk -v out="$split" 'BEGIN { n = 0; f = sprintf("%s/p%02d.yaml", out, n) } /^# ----$/ { close(f); n++; f = sprintf("%s/p%02d.yaml", out, n); next } { print > f }' "$fx/f.yaml"
[ "$(adm_fixture_pairs "$split/p01.yaml")" = "ryft-a${TAB}A: msg a" ] || fail "a single pair: $(adm_fixture_pairs "$split/p01.yaml")"
want=$(printf 'ryft-b\tB: msg b with '"'"'quotes'"'"' and a: colon\nryft-c\tC: msg c')
[ "$(adm_fixture_pairs "$split/p02.yaml")" = "$want" ] || fail "two pairs: $(adm_fixture_pairs "$split/p02.yaml")"
[ -z "$(adm_fixture_pairs "$split/p03.yaml")" ] || fail "an admitted fixture holds no pair"

# ---- every pair of the committed fixtures is a refusal of the harness ------------------------------------
golden="$HERE/infra/aws/kubernetes/admission-celtest/testdata/messages.golden"
grep -E '^    ryft-' "$golden" | sed 's/^    //' | sort -u > "$tmp/golden.txt"
# (the generated ones all; of the hand-written ones P12's: older fixtures act on objects the harness
# does not hold, a DELETE of default-deny among them)
for d in generated manual claims; do
  for f in "$FIXTURE_DIR/$d"/*.yaml; do
    adm_fixture_pairs "$f" | while IFS="$TAB" read -r b m; do
      [ -n "$b" ] || continue
      [ "$d" = generated ] || [ "$b" = ryft-argo-scope ] || continue
      grep -qxF "$b: $m" "$tmp/golden.txt" || { echo "$d/$(basename "$f"): $b: $m" >> "$tmp/unknown.txt"; }
    done
  done
done
if [ -s "$tmp/unknown.txt" ]; then fail "pairs in fixtures that testdata/messages.golden does not hold: $(head -3 "$tmp/unknown.txt")"; fi
# a generated fixture lists its bindings sorted, and its file is named after one of them
for f in "$FIXTURE_DIR"/generated/*.yaml; do
  bs=$(adm_fixture_pairs "$f" | cut -f1)
  [ "$bs" = "$(printf '%s\n' "$bs" | sort)" ] || fail "$(basename "$f"): bindings not sorted"
  ok=0; for b in $bs; do case "$(basename "$f")" in "$b"-[0-9][0-9]-*) ok=1 ;; esac; done
  [ "$ok" = 1 ] || fail "$(basename "$f") is named after none of its bindings: $bs"
done
# the P12 canary the design asks for: generated, refused by ryft-argo-scope only
c="$FIXTURE_DIR/generated/ryft-argo-scope-03-p12-controller-creates-a-deployment-in-cert-manager.yaml"
[ -f "$c" ] || fail "the cert-manager Deployment canary is not among the generated fixtures"
[ "$(adm_fixture_pairs "$c" | cut -f1)" = ryft-argo-scope ] || fail "the canary is refused by more than P12"
grep -q 'serviceAccountName: cert-manager' "$c" && grep -q 'namespace: cert-manager' "$c" || fail "the canary is not a Deployment in cert-manager as the cert-manager service account"
grep -q '^# as: system:serviceaccount:argocd:argocd-application-controller$' "$c" || fail "the canary is not created as the controller"

# ---- adm_fixtures: Deny takes one of the pairs, Audit wants every one ---------------------------------------
OUT1="" RC1=0 OUT2="" RC2=0 RCK=0
kca() { local who=$1; shift; case "$who" in u1) printf '%s\n' "$OUT1"; return "$RC1" ;; u2) printf '%s\n' "$OUT2"; return "$RC2" ;; esac; return 99; }
kc() { return "$RCK"; }
den() { printf "Error from server (Forbidden): admission webhook: ValidatingAdmissionPolicy 'p' with binding '%s' denied request: %s" "$1" "$2"; }
war() { printf "Warning: Validation failed for ValidatingAdmissionPolicy 'p' with binding '%s': %s\n" "$1" "$2"; }
MB="B: msg b with 'quotes' and a: colon"
run() { : > "$RESULTS"; adm_fixtures "$1" t "$split_dir"; }
split_dir=$tmp/fxdir; mkdir -p "$split_dir"; cp "$fx/f.yaml" "$split_dir/f.yaml"

OUT1=$(den ryft-a "A: msg a"); RC1=1; OUT2=$(den ryft-b "$MB"); RC2=1; RCK=0
run deny; expect fixtures-t-deny PASS "each fixture refused by its binding (the multi one by the first)"
OUT2=$(den ryft-c "C: msg c"); run deny; expect fixtures-t-deny PASS "the multi fixture refused by the second binding"
OUT2=$(den ryft-d "C: msg c"); run deny; expect fixtures-t-deny FAIL "the multi fixture refused by a binding it does not name"
OUT2=$(den ryft-c "C: another message"); run deny; expect fixtures-t-deny FAIL "the multi fixture refused with another message"
OUT2=$(den ryft-b "$MB"); RC2=0; run deny; expect fixtures-t-deny FAIL "the multi fixture admitted in Deny"
RC2=1; OUT1=$(den ryft-z "A: msg a"); run deny; expect fixtures-t-deny FAIL "the single fixture refused by another binding"
OUT1=$(den ryft-a "A: msg a"); RCK=1; run deny; expect fixtures-t-deny FAIL "the open fixture refused"
RCK=0

OUT1=$(war ryft-a "A: msg a"); RC1=0; OUT2=$(war ryft-b "$MB"; war ryft-c "C: msg c"); RC2=0
run audit; expect fixtures-t-audit PASS "Audit: every binding warns"
OUT2=$(war ryft-b "$MB"); run audit; expect fixtures-t-audit FAIL "Audit: only the first binding warns"
OUT2=$(war ryft-c "C: msg c"); run audit; expect fixtures-t-audit FAIL "Audit: only the second binding warns"
OUT2=$(war ryft-b "$MB"; war ryft-c "C: msg c"); OUT1=""; run audit; expect fixtures-t-audit FAIL "Audit: the single fixture is silent"
OUT1=$(war ryft-a "A: msg a")
RC2=1; OUT2="$(war ryft-b "$MB"; war ryft-c "C: msg c") Error from server (AlreadyExists): configmaps x already exists"; run audit; expect fixtures-t-audit PASS "Audit: AlreadyExists after the warnings is fine"
RC2=0

# ---- adm_argo_scope_secretkey --------------------------------------------------------------------------------
KEY="$tmp/key"; rm -f "$KEY"
ARGO_SRV=system:serviceaccount:argocd:argocd-server
MSG3="P12: argocd-server writes only Applications and events in argocd (and once the first server.secretkey of argocd-secret)."
MODE_SK=deny BREAK=
kca() {
  local who=$1; shift
  [ "$who" = "$ARGO_SRV" ] || return 98
  local all="$*" dry=0
  case "$all" in *--dry-run=server*) dry=1 ;; esac
  if [ -f "$KEY" ]; then
    # the key exists: any write of it is refused (Deny) or warned about (Audit)
    if [ "$MODE_SK" = deny ]; then den ryft-argo-scope "$MSG3"; return 1; fi
    war ryft-argo-scope "$MSG3"; [ "$dry" = 1 ] || : > "$KEY"; return 0
  fi
  if [ -n "$BREAK" ]; then den ryft-argo-scope "$MSG3"; return 1; fi   # the exemption does not work
  [ "$dry" = 1 ] || : > "$KEY"
  return 0
}
kc() {
  case "$*" in
    *"patch secret argocd-secret"*) rm -f "$KEY"; return 0 ;;
    *"get secret argocd-secret"*) if [ -f "$KEY" ]; then echo '{"data":{"admin.password":"x","server.secretkey":"Yw=="}}'; else echo '{"data":{"admin.password":"x"}}'; fi; return 0 ;;
  esac
  return 97
}
MODE_SK=deny; : > "$RESULTS"; adm_argo_scope_secretkey deny
expect argo-scope-secretkey-deny PASS "the first key admitted, its change and removal refused, the key removed again"
[ ! -f "$KEY" ] || fail "the stand-in is left with the key"
MODE_SK=audit; : > "$RESULTS"; adm_argo_scope_secretkey audit
expect argo-scope-secretkey-audit PASS "Audit: the same, as warnings"
BREAK=1; MODE_SK=deny; : > "$RESULTS"; adm_argo_scope_secretkey deny
expect argo-scope-secretkey-deny FAIL "the first key refused (the exemption broken)"
BREAK=
# a Deny that lets the change through
kca() { local who=$1; shift; case "$*" in *--dry-run=server*) return 0 ;; *) [ -f "$KEY" ] || : > "$KEY"; return 0 ;; esac; }
: > "$RESULTS"; adm_argo_scope_secretkey deny
expect argo-scope-secretkey-deny FAIL "a change of the key admitted in Deny"

# ---- adm_dryrun_p12 ---------------------------------------------------------------------------------------------
adm_apps() { printf 'a\tx\nb\ty\n'; }
printf 'vault-api\tok\tConfigMap ryft-api/c\t\t\nryft-root\tok\tApplication argocd/vault-api\t\t\nargocd-hand\tok\tAppProject argocd/ryft\t\t\n' > "$WORK/dry-all-deny.tsv"
: > "$RESULTS"; adm_dryrun_p12 deny; expect dryrun-deny-argo-scope PASS "no P12 line"
printf 'vault-api\trefused\tConfigMap kube-system/c\tryft-argo-scope\tP12: x\n' >> "$WORK/dry-all-deny.tsv"
: > "$RESULTS"; adm_dryrun_p12 deny; expect dryrun-deny-argo-scope FAIL "a refusal by P12"
printf 'vault-api\tok\tConfigMap ryft-api/c\t\t\nargocd-hand\twarned\tAppProject argocd/ryft\tryft-argocd\tP7: y\n' > "$WORK/dry-all-audit.tsv"
: > "$RESULTS"; adm_dryrun_p12 audit; expect dryrun-audit-argo-scope PASS "another policy's warning is not P12's"
printf 'vault-api\twarned\tConfigMap kube-system/c\tryft-argo-scope\tP12: x\n' >> "$WORK/dry-all-audit.tsv"
: > "$RESULTS"; adm_dryrun_p12 audit; expect dryrun-audit-argo-scope FAIL "a warning by P12"

# ---- adm_check_argo_scope_policy ------------------------------------------------------------------------------------
pol() { # <resources json> <scope> <observed generation> <typeChecking json>
  jq -nc --argjson r "$1" --arg s "$2" --argjson g "$3" --argjson t "$4" '{items: [{metadata: {name: "ryft-argo-scope", generation: 1}, spec: {matchConstraints: {resourceRules: [{resources: $r, scope: $s}]}}, status: ({observedGeneration: $g} + (if $t == null then {} else {typeChecking: $t} end))}]}'
}
: > "$RESULTS"; adm_check_argo_scope_policy audit "$(pol '["*/*"]' '*' 1 '{}')"; expect argo-scope-policy-audit PASS "stored as rendered, typeChecking empty"
: > "$RESULTS"; adm_check_argo_scope_policy audit "$(pol '["*","*/*"]' '*' 1 '{}')"; expect argo-scope-policy-audit FAIL "the sketch's resources"
: > "$RESULTS"; adm_check_argo_scope_policy audit "$(pol '["*/*"]' 'Namespaced' 1 '{}')"; expect argo-scope-policy-audit FAIL "another scope"
: > "$RESULTS"; adm_check_argo_scope_policy audit "$(pol '["*/*"]' '*' 0 '{}')"; expect argo-scope-policy-audit FAIL "not observed"
: > "$RESULTS"; adm_check_argo_scope_policy audit "$(pol '["*/*"]' '*' 1 'null')"; expect argo-scope-policy-audit FAIL "no typeChecking"
: > "$RESULTS"; adm_check_argo_scope_policy audit "$(pol '["*/*"]' '*' 1 '{"expressionWarnings":[{"fieldRef":"spec.validations[0].expression","warning":"x"}]}')"; expect argo-scope-policy-audit FAIL "a type-checking warning"
: > "$RESULTS"; adm_check_argo_scope_policy audit '{"items":[]}'; expect argo-scope-policy-audit SKIP "a chart without P12"

# ---- adm_full_p12_findings (claim n) ---------------------------------------------------------------------------------------
AUDIT=$tmp/audit AUDIT_RC=0 CREATED=1
adm_audit_grep() { [ "$AUDIT_RC" = 0 ] || return "$AUDIT_RC"; [ -z "$AUDIT" ] || cat "$AUDIT"; }
NEW=2026-10-26T12:00:01.000000Z OLD=2026-10-26T11:00:00.000000Z SINCE=2026-10-26T12:00:00.000000Z
CTRL=system:serviceaccount:argocd:argocd-application-controller
ann() { # <user> <resource> <actions> <timestamp>
  jq -nc --arg u "$1" --arg r "$2" --arg a "$3" --arg t "$4" '{requestReceivedTimestamp: $t, verb: "create", user: {username: $u}, objectRef: {resource: $r, namespace: "kube-system", name: "x"}, responseStatus: {code: 200},
    annotations: {"validation.policy.admission.k8s.io/validation_failure": ([{message: "P12: x", policy: "ryft-argo-scope", binding: "ryft-argo-scope", expressionIndex: 3, validationActions: ($a | split("+"))}] | tojson)}}'
}
ref() { # <user> <timestamp>: a refusal under Deny
  jq -nc --arg u "$1" --arg t "$2" '{requestReceivedTimestamp: $t, verb: "delete", user: {username: $u}, objectRef: {resource: "deployments", namespace: "kube-system", name: "coredns"},
    responseStatus: {code: 403, message: "admission webhook: ValidatingAdmissionPolicy '"'"'ryft-argo-scope'"'"' with binding '"'"'ryft-argo-scope'"'"' denied request: P12: x"}}'
}
helm_binding() { jq -nc --arg t "$1" '{requestReceivedTimestamp: $t, verb: "create", user: {username: "kubernetes-admin"}, objectRef: {resource: "validatingadmissionpolicybindings", name: "ryft-argo-scope"}, responseStatus: {code: 201}}'; }
other_403() { jq -nc --arg t "$1" '{requestReceivedTimestamp: $t, verb: "create", user: {username: "x"}, objectRef: {resource: "pods"}, responseStatus: {code: 403, message: "ValidatingAdmissionPolicy '"'"'ryft-netpol'"'"' denied request"}}'; }
f12() { : > "$RESULTS"; adm_full_p12_findings "claim-n" "$SINCE"; }
: > "$AUDIT"; f12; expect claim-n PASS "an empty log"
{ helm_binding "$NEW"; other_403 "$NEW"; ann "$CTRL" configmaps Warn+Audit "$OLD"; ref "$CTRL" "$OLD"; } > "$AUDIT"; f12; expect claim-n PASS "the policy's own binding, another policy's refusal and old lines are no findings"
ann "$CTRL" configmaps Warn+Audit "$NEW" > "$AUDIT"; f12; expect claim-n FAIL "a warning of P12 in Audit"
ref "$CTRL" "$NEW" > "$AUDIT"; f12; expect claim-n FAIL "a refusal by P12 in Deny"
case "$(detail claim-n)" in *"$CTRL"*) ;; *) fail "the finding does not name the requester: $(detail claim-n)" ;; esac
AUDIT_RC=2; CREATED=1; f12; expect claim-n FAIL "no audit log on a cluster the run created"
CREATED=0; f12; expect claim-n SKIP "no audit log on a cluster handed over"
AUDIT_RC=0; CREATED=1

# ---- adm_full_claim_o -----------------------------------------------------------------------------------------------------------
adm_full_p12_stage() { :; }
ARGO_CTRL=$CTRL
SSAR_CTL="yes" SSAR_SRV=""
kca() { case "$1" in "$ARGO_CTRL") printf '%s\n' "$SSAR_CTL" ;; "$ARGO_SRV") printf '%s\n' "$SSAR_SRV" ;; esac; return 0; }
co() { : > "$RESULTS"; adm_full_claim_o "" || true; }
SSAR_CTL=no SSAR_SRV=yes; co; expect claim-o-canary PASS "neither review was refused: access reviews bypass validating admission"
SSAR_CTL=yes SSAR_SRV=$(den ryft-argo-scope "$MSG3"); co; expect claim-o-canary FAIL "the server's review was refused: admission sees access reviews"
SSAR_CTL=$(den ryft-argo-scope "P12: Argo CD's controller writes cluster-scoped objects only of the kinds and names its apps render"); SSAR_SRV=yes; co; expect claim-o-canary FAIL "the controller's review refused"
SSAR_CTL="error: the server could not find the requested resource" SSAR_SRV=yes; co; expect claim-o-canary FAIL "the controller's review gave no answer"

# ---- adm_full_argo_restart (claim p) ----------------------------------------------------------------------------------------------
RV=100 RV_AFTER=100 ROLL_RC=0
adm_full_argocd_login() { return 0; }
adm_full_wait_apps() { :; }
adm_full_findings() { :; }
unset -f adm_full_p12_stage
adm_full_p12_stage() { :; }
kc() {
  # (the first read of the Secret's resourceVersion gives RV, the second RV_AFTER; a counter file, because
  # the reads run in command substitutions)
  case "$*" in
    *"get secret argocd-secret"*) echo x >> "$tmp/rvreads"; if [ "$(wc -l < "$tmp/rvreads" | tr -d ' ')" = 1 ]; then printf '%s' "$RV"; else printf '%s' "$RV_AFTER"; fi ;;
    *"rollout restart"*) return 0 ;;
    *"rollout status"*) return "$ROLL_RC" ;;
    *) return 97 ;;
  esac
}
rr() { : > "$RESULTS"; : > "$tmp/rvreads"; RV=${1:-100}; RV_AFTER=${2:-100}; adm_full_argo_restart "r" "" || true; }
rr 100 100; expect claim-p PASS "argocd-secret untouched by the restart"; expect r PASS "the rollout"
rr 100 101; expect claim-p FAIL "argocd-secret written during the restart"
ROLL_RC=1; rr 100 100; expect r FAIL "the rollout did not finish"; ROLL_RC=0

# ---- adm_full_argo_upgrade: the redis-secret-init hook ran if the Job is a new object ------------------------------------------------
adm_full_argocd() { echo "${2:-}" > "$tmp/argocd_force"; rec "$1" PASS "stub"; }
UID0=u1 UID1=u2 SUCC=1
kc() {
  case "$*" in
    *"get job argocd-redis-secret-init"*uid*) echo x >> "$tmp/uidreads"; if [ "$(wc -l < "$tmp/uidreads" | tr -d ' ')" = 1 ]; then printf '%s' "$UID0"; else printf '%s' "$UID1"; fi ;;
    *"get job argocd-redis-secret-init"*succeeded*) printf '%s' "$SUCC" ;;
    *) return 97 ;;
  esac
}
up() { : > "$RESULTS"; : > "$tmp/uidreads"; adm_full_argo_upgrade "u" "" || true; }
up; expect u-hook PASS "a new hook Job that succeeded"
[ "$(cat "$tmp/argocd_force")" = force ] || fail "the upgrade must pass force (the harness's kubectl-patch owns the admin password)"
UID1=u1; up; expect u-hook INFO "the same Job as before the upgrade: the hook did not run"
UID0="" UID1=u2 SUCC=0; up; expect u-hook INFO "a new Job that did not succeed"

echo "✓ admission_fixtures_test"

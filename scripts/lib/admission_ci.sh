#!/usr/bin/env bash
# shellcheck shell=bash
# The checks of the CI job `admission` in order (scripts/admission_e2e.sh --mode ci; --mode full runs
# them first). Sourced after kind_guard.sh and admission_lib.sh.

# The objects the fixtures act on, applied by the admin before any policy exists: the projects, the
# root and the applications of deploy/argocd (nothing syncs them here: no controller runs in this
# part), the baseline of the fixtures, and an operation in flight on vault-smoke for terminate-op.
adm_baseline() {
  if kc apply -f "$REPO/deploy/argocd/project.yaml" -f "$REPO/deploy/argocd/root.yaml" -f "$REPO/deploy/argocd/apps/" >"$WORK/log/baseline.out" 2>&1 \
    && kc apply -f "$FIXTURE_DIR/baseline/objects.yaml" >>"$WORK/log/baseline.out" 2>&1 \
    && kc -n argocd patch application vault-smoke --type merge -p '{"operation":{"sync":{"revision":"main"}}}' >>"$WORK/log/baseline.out" 2>&1; then
    rec "baseline" PASS "projects, root, $(adm_apps | wc -l | tr -d ' ') applications, default-deny and a repository Secret applied by the admin before the policies"
  else
    rec "baseline" FAIL "$(tail -3 "$WORK/log/baseline.out")"
  fi
}

# adm_unbaseline: the same objects removed again (the full mode starts Argo CD on a clean cluster).
adm_unbaseline() {
  kc -n argocd patch application vault-smoke --type merge -p '{"operation":null}' >/dev/null 2>&1
  kc delete -f "$FIXTURE_DIR/baseline/objects.yaml" --ignore-not-found >/dev/null 2>&1
  kc delete -f "$REPO/deploy/argocd/apps/" -f "$REPO/deploy/argocd/root.yaml" --ignore-not-found >/dev/null 2>&1
  kc -n argocd delete appproject ryft ryft-operators ryft-root --ignore-not-found >/dev/null 2>&1
}

# Which refusals of the harness's golden file have a fixture on the real API server?
adm_fixture_coverage() {
  local golden="$ROOT/$K8S_DIR_REL/admission-celtest/testdata/messages.golden" have="$WORK/fixture-messages.txt" want="$WORK/golden-messages.txt" missing
  cat "$FIXTURE_DIR"/generated/*.yaml "$FIXTURE_DIR"/manual/*.yaml "$FIXTURE_DIR"/claims/*.yaml 2>/dev/null \
    | awk '/^# refused-by: / { b = substr($0, 15) } /^# message: / && b != "" { print b ": " substr($0, 12); b = "" }' | sort -u > "$have"
  grep -E '^    ryft-' "$golden" | sed 's/^    //' | sort -u > "$want"
  missing=$(comm -13 "$have" "$want" | wc -l | tr -d ' ')
  rec "fixture-coverage" INFO "$(wc -l < "$want" | tr -d ' ') distinct refusals pinned by the CEL harness; $(comm -12 "$have" "$want" | wc -l | tr -d ' ') have a fixture on the API server, $missing are held by the harness only (UPDATEs with an old object, Pod subresources, objects the CRDs would reject): $(comm -13 "$have" "$want" | cut -d: -f1-2 | sort | uniq -c | tr -s ' ' | tr '\n' ';')"
}

adm_ci_checks() {
  adm_baseline
  adm_claim_h open
  group "policies in Audit (the committed values)"
  adm_chart_install audit
  adm_dryrun_apps audit
  adm_fixtures audit generated "$FIXTURE_DIR/generated"
  adm_fixtures audit manual "$FIXTURE_DIR/manual"
  adm_claim_d
  endgroup
  group "policies in Deny (every binding)"
  adm_chart_install deny
  adm_dryrun_apps deny
  adm_fixtures deny generated "$FIXTURE_DIR/generated"
  adm_fixtures deny manual "$FIXTURE_DIR/manual"
  adm_claim_a
  adm_claim_b
  adm_claim_c
  adm_claim_h guarded
  endgroup
  adm_fixture_coverage
}

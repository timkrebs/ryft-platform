# Rollback drill — services (ticket F)

Prepared by sre (read-only; the edit was simulated in a scratch clone), run by the lead, every merge approved by the
user. Written for "current pin → previous pin"; values below are 2026-10-22's (services `a9685b5` → `0fd4256`, web
`bfc51b8`). If C has moved the services pin, recompute with 1.0: the drill then rolls C's backend change back too.

## 1. Preconditions (read-only, right before)

1.0 Current and previous pin: `git log --format='%h %s' -8 -- deploy/k8s/overlays/eks/kustomization.yaml`, take the
last "deploy: images X" that moved the **services** (web-only deploys are listed too); `git show <merge>^:<overlay>`
must show the same old `newTag` for all five services images.

1.1 Nothing pending: `./scripts/deploy_targets.sh` → `services=false web=false held=false`; no `# hold:` line;
`gh pr list --state open --search 'head:deploy/'` and `git ls-remote --heads origin 'deploy/*'` empty; the last CI run
on main green, none in progress; `GH_REPO=timkrebs/ryft-platform ./scripts/deploy_watch.sh` → OK. After B: the last
`promote` succeeded in environment `deploy` (`gh secret list --env deploy -R timkrebs/ryft-platform`, the branch
policy `main`).

1.2 Argo: `kubectl -n argocd get applications` all Synced/Healthy; `ryft-platform`'s `status.sync.revision` = origin/main,
`operationState.phase` Succeeded.

1.3 Target images: `./scripts/check_registry.sh ghcr.io/timkrebs/ryft/{ingest,scoring,api,notify,migrate}:<previous>`.

1.4 Before-snapshot: per namespace `ryft-{ingest,scoring,api,notify,web}` the Deployment's ready/replicas and image
(today ingest 2/2, api 2/2, scoring 1/1, notify 1/1, web 2/2); the ReplicaSets; `ryft-smoke` Job timestamp and image;
pods Running, 0 restarts.

1.5 Edge and broker: `curl -s -o /dev/null -w '%{http_code}' https://api.ryftapp.de/v1/me` → 401; `kubectl -n
ryft-rabbitmq get pods` 3/3; `scripts/dlq.sh list` baseline; no firing alert; `make smoke-prod` if its env is set.

Go/no-go: any red item, or another merge planned during the drill → don't start.

## 2. Rollback PR

Branch `fix/rollback-services`, as in the runbook (never `deploy/<7hex>`; a `deploy/…` name would also match the
precondition search). Follow OPERATIONS → Deploy and roll back as written and record every deviation. In the overlay:
the five services `newTag`s to the previous pin (quoted), then `./scripts/hold.sh add services "rollback drill,
previous pin"` (writes the hold line above the ingest entry, UTC date) — six changed lines.

Local checks: `HEAD_REF=fix/rollback-services ./scripts/deploy_targets.sh --check origin/main` (rc 0, notice "this PR
holds services"); `./scripts/deploy_targets.sh` (`services=false services_pin=<previous> … held=true`, warning "1
commits under backend/ since <previous>, not deployed"); `make k8s-validate`; `kubectl kustomize
deploy/k8s/overlays/eks | grep 'image: ghcr' | sort | uniq -c`; `./scripts/check_registry.sh`.

Commit `deploy: roll back services to <previous>`, `gh pr create --fill`, **no `--auto`** (main has no required
review: auto-merge would skip the user's approval). CI: `changes`, `manifests` (hold in the summary, registry ✓),
`secrets`, `pr-title`, `ci-ok`; ~3–5 min.

On merge: `promote` runs (held=true) and opens nothing; Argo picks the merge up within ~3 min: PreSync `migrate` with
the **old** `migrate:<previous>` image (same migrations: a no-op), all Deployments at once (no waves; RollingUpdate
25 %/25 %; PDB minAvailable 1 on api/ingest), PostSync `smoke` with the old image. Merge → PostSync done ≈ 2–6 min;
abort if no new operation 10 min after the merge.

Verify (within the hour — the migrate Job has a 1 h TTL; hook results stay in Argo's operation state):

```bash
kubectl -n argocd get app ryft-platform -o jsonpath='{.status.operationState.phase}{" "}{.status.operationState.syncResult.revision}{"\n"}{range .status.operationState.syncResult.resources[?(@.hookType)]}{.kind}/{.name} {.hookType} {.hookPhase}{"\n"}{end}'
for n in ingest scoring api notify web; do kubectl -n ryft-$n get deploy $n -o jsonpath='{.metadata.namespace} {.status.readyReplicas}/{.spec.replicas} {.spec.template.spec.containers[0].image}{"\n"}'; done
for n in ingest scoring api notify; do kubectl -n ryft-$n get rs -o 'custom-columns=NS:.metadata.namespace,NAME:.metadata.name,DES:.spec.replicas,IMG:.spec.template.spec.containers[0].image'; done
kubectl -n ryft-smoke get job smoke -o jsonpath='{.metadata.creationTimestamp} {.status.succeeded} {.spec.template.spec.containers[0].image}{"\n"}'
kubectl -n ryft-scoring logs deploy/scoring --since=15m | grep -E 'amqp connected|consuming'
kubectl -n ryft-notify logs deploy/notify --since=15m | grep -E 'amqp connected|consuming'
curl -s -o /dev/null -w '%{http_code}\n' https://api.ryftapp.de/v1/me
```

Expect: services on `<previous>`, web unchanged, one RS with replicas per service, hooks Succeeded, 0 restarts, 401,
consumers connected, no new dead letters.

## 3. Proof the hold works

The rollback merge is itself the push: its run shows `images`/`web-image` skipped, `promote` success with
`promote: no group differs from its running pin (services: held since …)`, and afterwards no `deploy/<sha>` branch or
PR. Then `gh workflow run deploy-watch` (asks first): expect OK with the hold listed as within 7 days and no
"targeted without a PR" finding. Nothing else merges during the hold window.

## 4. Release

Branch `fix/release-services`: `./scripts/hold.sh rm services`, nothing else. `./scripts/deploy_targets.sh` → `services=true …
backend/ changed since the pin`; `--check` notices the release. Commit `deploy: release the services hold`, merge by
hand after approval. Two merges follow from it: the release merge builds five images at its own SHA `M`, promote opens
`deploy/M` (`### services: <previous> → M`) with auto-merge; pods on `M` ≈ 12–15 min after the release merge. Verify as
in 2 with `M`; `deploy_targets.sh` → `services=false held=false`; the pin commit's own run builds nothing. Record the
times of both halves.

## 5. Abort and the way back

| Step | Abort if | Way back |
| --- | --- | --- |
| Preconditions | any red item | don't start |
| Before merge | local check or CI red, no approval | close the PR, delete the branch |
| PreSync | migrate fails/hangs | nothing rolled; Argo retries; then the undo PR |
| Rollout | CrashLoop/Unready > 3 min, api or ingest < 1 ready | old RS keeps serving; undo PR |
| PostSync / verify | smoke fails, `/v1/me` ≠ 401, DLQ grows, ArgoSyncFailed | undo PR |
| Hold window | a deploy PR appears | close it; undo PR; CI defect |
| Release | images/promote fail | fix, "Re-run failed jobs"; cluster safe on `<previous>` |
| After deploy merge | rollout/smoke failure | pin `<previous>` + hold again |

Undo PR: one commit on a non-hex branch that sets the five tags back to the pre-drill pin and runs
`./scripts/hold.sh rm services`. If `backend/` moved past the pre-drill pin meanwhile, its merge builds and deploys a
new SHA instead.

## 6. Selective sync and hooks (STATUS question)

A selective sync (`argocd app sync ryft-platform --resource apps:Deployment:web`) **skips the hooks** and is not
recorded in the app's history. Argo CD v3.5.6 (the version we run, `infra/aws/kubernetes/argocd.tf`),
`docs/user-guide/selective_sync.md`: "Hooks are **not** run."; `docs/user-guide/sync-waves.md`: "Hooks do not run
during a selective sync operation."; `controller/sync.go` passes `skipHooks` when `len(syncOp.Resources) > 0`. Use it
only when migrate blocks and the change is safe without migrate and smoke; user-only.

## 7. What the drill does not prove

`0fd4256` and `a9685b5` share their migrations, so "the previous release runs on the newer schema" is not exercised.
Runbook gaps found while preparing were closed in B (#21, OPERATIONS → Deploy and roll back); the drill follows that
text and records every deviation from it.

## Record (2026-10-22, run by the lead, each merge approved by the user)

Pins at the start: services `d6596fe` (F1's deploy, #25), web `d6596fe`; the drill rolled the services back to the
previous pin `a9685b5` (differs only by F1's Go start-up check; migrations identical).

| Step | Time (UTC) | Result |
| --- | --- | --- |
| Preconditions | 17:32 | `deploy_targets.sh` nothing pending, no hold, no deploy PR or branch, `deploy_watch.sh` OK, 11 Argo apps Synced/Healthy, the five `a9685b5` images in GHCR; main's last run had to finish first (#25's merge run). |
| Rollback PR #26 | 17:33 open, 17:35:21 merged | `pin_images.sh a9685b5 services` + `hold.sh add services "rollback drill, previous pin"`: six lines, as the runbook says. CI: `manifests` (hold notice, registry), `ci-ok`, `pr-title` in ~1.5 min. Merged by hand. |
| Argo sync | 17:37:41–17:38:11 | 2 min 20 s after the merge; 30 s sync. PreSync migrate `a9685b5` ("schema current"), the four services on `a9685b5`, 0 restarts, web untouched, PostSync smoke Succeeded, `/v1/me` 401, scoring and notify "amqp connected/consuming". |
| Hold proof | 17:41 | main's run: `changes` and `promote` warn "services held since 2026-10-22 …, 1 commits under backend/ since a9685b5, not deployed"; promote: "no group differs from its running pin (services: held …)"; no deploy PR, no `deploy/*` branch. |
| Watch during the hold | 17:43 | `gh workflow run deploy-watch`: "OK: 1 hold(s) within 7 days, 0 deploy PR(s) open". |
| Release PR #27 | 17:42 open, 17:45:42 merged | `hold.sh rm services` only; `--check` notice "releases the hold"; CI green. |
| Release merge's run | 17:48:36–17:53:15 | Argo marked `315a913` Synced without an operation (only a comment changed); main's run: every job green, five service images built for `315a913`, promote opened #28 "services: `a9685b5` → `315a913` (backend/ changed since the pin)", web unchanged. |
| Deploy PR #28 | 17:53:21 open, 17:54:25 merged | merged itself after its checks (~1 min). |
| Argo sync | 18:00:33 | **6 min after the merge** (the runbook says ~3: Argo's poll plus repo cache lag; within the 10-minute abort line). Services on `315a913`, PreSync migrate and PostSync smoke Succeeded, `/v1/me` 401. |

Total: rollback merge → services back on the old pin ≈ 3 min; release merge → services on the new SHA ≈ 15 min, as
OPERATIONS says. The hold, the watch and promote behaved exactly as documented; one runbook number (Argo pickup "about
3 minutes") is optimistic — 2–6 minutes observed.

Open after the drill: Argo CD reports `OrphanedResourceWarning: 1 orphaned resource` on ryft-platform (seen at 17:59;
not identified — `argocd app resources ryft-platform --orphaned` needs an Argo login).

**Found by the drill:** while a hold is in place, main's `smoke` job is red: `deploy_targets_test` copies the real
overlay and asserts `held=false` ("held=false without a hold: true"). So during a hold `images` (which needs `smoke`)
cannot build, and a fix PR that runs `smoke` is red too — exactly when a fix is needed. Not hit by the release (the
release removes the hold). → fixed in H (tests must not read the live overlay's hold state).

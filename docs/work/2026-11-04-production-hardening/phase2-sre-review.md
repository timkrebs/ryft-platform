# Phase 2: SRE review of A1 (`internal/app`, PR #105) and the staged rollout

> **Record, as written 2026-11-04.** A1 (#105) and A2 (#117, with the `Panics` extension and `ServiceExited`) are
> merged. The staged-rollout checklists below are the plan as reviewed; on 2026-11-05 A2's promote PR (#124) was
> superseded by the next promote PR (#130), which merged itself: see D175 for what happened and for the rule that
> replaced "do not merge the promote PR". A3 to A7 are not built.

Source: sre review of PR #105 on 2026-11-04 (read-only; repo and manifests only, no cluster access).
Verdict: A1 is safe to merge as far as runtime goes (nothing calls it). B1 and B2 are fixed in A1 because
they are constants and `Spec` semantics that A2..A5 build on. B3 gates A2.

## Blockers

- **B1 budget.** `stopHTTP` is Shutdown (10 s) + `serve.wait` (3 s) = 13 s, not 10 s; the metrics stop (3 s) is in no
  budget line. Worst case from the code: api/ingest after A5 13+3+3+2+3+3 = 27 s; with A7 (5 s drain delay) 32 s;
  scoring/notify 15+3+3+2+3+3 = 29 s, against a 30 s grace period. Fix in A1: a watchdog (atomic current step,
  `time.AfterFunc(grace-4 s)` logs `shutdown overdue` with `step`, `elapsed_ms`, then exits 1); tighten `serve.wait`
  and the metrics stop to 1 s; write the sum honestly. A5's test compares `Spec.Grace` with the manifests.
- **B2 `ConsumerDrain` is two bounds.** amqpbus `Drain` (when the handler context is cancelled) must be shorter than the
  tier wait (`ConsumerDrain` = 15 s), else the step times out every time. Drain = ConsumerDrain - 2 s, or the tier
  waits ConsumerDrain + 2 s; say which in the `Spec` comment; keep inside B1's sum.
- **B3 a task panic is invisible.** The counter increments in a process that dies and resets on restart; alert must
  not rely on it alone. Gate for A2 (first emitter): (1) extend `Panics` with `or` (not `+`):
  `sum(increase(ryft_http_panics_total[15m])) > 0 or sum(increase(ryft_goroutine_panics_total[15m])) > 0`;
  (2) a restart-based `ServiceExited` alert (restarts in ryft-(api|ingest|scoring|notify) with last exit code 1);
  (3) `runbook:` annotations and promtool tests in `ryft.test.yaml` for both; (4) fix existing gaps in `Panics`
  (first panic per route is born at 1: use the `or ... unless last_over_time(... offset 1h)` pattern of
  TextRefusedByDatabase; `ryft_event_handler_panics_total{subject}` is ignored; no annotations);
  (5) the counter stays label-free (update the design: no `{task}`).

## Should fix

- S1 Jobs and Consumers must not return transient errors (a returned error stops the service); log and swallow, return
  only fatal ones; put it in the `Task` doc comment and the A2/A3 checklists; per-iteration `ctx.Err()` checks.
- S2 Exit codes of rolled pods never reach Grafana (kube-state-metrics drops deleted pods): "exit 1 on overrun" is
  observable in CloudWatch only; fix the sentence in the design and OPERATIONS.
- S3 SIGTERM during `Build` (or the db/bus wait) does nothing until it returns: while `start` runs, end the life
  context on `ctx.Done()`.
- S4 Silent failures: log errors arriving after `stop` in `tier.run` (warn, no fail); log the metrics-step failure;
  count and log `safeCheck` panics; `reason` in `shutting down` is a closed set (`signal`, `task`, `start`) with
  `task` and a capped `err` separately (a pgx encode error can quote a value).
- S5 Stuck start is silent: A5's "metrics first" must also set `ryft_db_up{service}=0` and `ryft_amqp_connected=0`
  before the waits (the startup probe kills at 630 s, so `PodRestarting` never fires).
- S6 Readiness probes have `timeoutSeconds: 1` but /readyz allows checks 2 s: A5 sets `timeoutSeconds: 3`; pool
  saturation (import 50 s, deletion 2 min, recompute) reads as `db: down`: distinguish via `pool.Stat()`.
- S7 A4: the `subscribe` loop can still start up to 9 prefetched deliveries after ctx ends (random `select`): add a
  `ctx.Err()` check at the top; `result="requeued"` is incremented in a dying process, so the A4 go/no-go uses the
  log line `delivery requeued at shutdown`; drop scoring prefetch to 1 in A4.
- S8 Staging reality: the five backend images are one group (D107); every `backend/` merge rolls all five services;
  merging A2 and A3 close together collapses them into one deploy. To stage: merge one, wait for the deploy PR and
  Argo (about 15 min), soak, then the next. Control: close the deploy PR and `hold.sh add services`.
- S9 A7 (5 s drain delay): Traefik's endpoint removal takes about 2-4 s; 5 s is the floor; keep A7 last and
  separate; use the external edge probe.

## Nits

`Stale()` unused until Phase 3 (cap staleness, e.g. > 24 h counts as down); second signal swallowed until A5; a bind
failure on :9090 is only logged; explicit `strategy: RollingUpdate` in the four Deployments; doc.go "every goroutine
is a Task" is too strong; A5 adds `task`/`step` to the logcheck allow-lists (D117); the /readyz body shows hosts
(cluster-internal only; security review in A5); cap non-required checks at about 500 ms.

## Operational facts (verified in the manifests)

- Liveness/startup probes use `/healthz`, readiness `/readyz`; nothing restarts a pod because of `/readyz`.
- Every reader of `/readyz` looks at the status code only.
- No Deployment sets `terminationGracePeriodSeconds`, `preStop` or `strategy` (defaults: 30 s, RollingUpdate).
- Required checks: api/ingest db+auth (broker reported only, D101); scoring/notify db+broker.
- Scoring/notify roll with surge 1: two pods run for about 5-30 s (D100 idempotence covers it); keep the default surge.
- Node drain: api/ingest have PDBs; scoring/notify do not (correct); consumer gap about 30-60 s.
- Rollback for every stage: `./scripts/pin_images.sh <old-sha> services && ./scripts/hold.sh add services "<reason>"`
  in one commit, merged as a PR; never pin ingest before the D144/D145 one-way door; the user runs it.

## CloudWatch Insights (log group `/ryft/ryft-prod-euc1/containers`, fields under `data.`)

```
# the shutdown timeline of one deploy
fields @timestamp, data.service, data.msg, data.reason, data.step, data.dur_ms, data.exit, data.err
| filter data.msg in ["shutting down","shutdown step","shut down","shutdown overdue"]
| sort @timestamp asc

# pods that began shutdown but never finished (SIGKILL): expect no rows
fields @logStream, data.msg
| filter data.msg in ["shutting down","shut down"]
| stats sum(strcontains(data.msg,"shutting down")) as began, sum(strcontains(data.msg,"shut down")) as done by @logStream
| filter began > 0 and done = 0

# slow or failed steps
fields @timestamp, data.service, data.step, data.dur_ms, data.err
| filter data.msg = "shutdown step" and (data.dur_ms > 5000 or ispresent(data.err))
| sort data.dur_ms desc

# consumer side (A4 baseline and after)
fields @timestamp, data.service, data.msg, data.queue, data.attempt, data.err
| filter data.service in ["scoring","notify"] and (data.msg like /could not (retry|dead-letter|defer)/ or data.msg = "handle event failed, will retry" or data.msg = "delivery requeued at shutdown")
| sort @timestamp desc
```

## Staged rollout checklists (paste into the PR bodies; replace $CTX with `ryft-prod-euc1`)

**A2 (scoring + notify through app.Run).** Pure refactor except: an HTTP bind failure now exits 1.
Before merge: #53 merged or closed; A1 deployed; last deploy green. Task bodies swallow transient errors. Panics
alert extended and ServiceExited added with runbook annotations and tests; `make alerts-check` green.
Go (30 min after Healthy): smoke Succeeded; restarts of ryft-(scoring|notify) == 0 over 30 m; `ryft_amqp_connected`
== 1 and `ryft_db_up` == 1; queues scoring/notify back near 0 within 5 min; sync-to-score p95 < 120 s;
`increase(ryft_goroutine_panics_total[30m]) == 0`; logs show "listening" and "consuming ..." without a new "serve"
error. No-go: any restart, queue above 100 for 15 min, smoke failed, unexpected "serve" error: roll back.

**A3 (api + ingest through app.Run).** No behaviour change; `TestRoutes` written from today's main in the first
commit; `make check-swift` green; Jobs (reconciler) never return transient errors; A2 deployed and soaked at least
1 h; no import in flight (CloudWatch `data.msg = "train import"` in the last 5 min).
During: keep the edge probe running (expect only 401 on `/v1/me`; confirm on a quiet day first) and probe a path
routed to ingest. Go: smoke + `make smoke-prod` green; 5xx ratio of ryft-(api|ingest) < 0.5 % over 30 m; replicas
available == spec (at least 2); restarts 0; HPA back at 2; POST /v1/sync p95 within 20 % of the pre-deploy hour.
No-go: 5xx > 1 % for 5 min, a known path 404, smoke failed, restarts > 0.

**A4 (amqpbus finishes the delivery in hand).** Go/no-go uses logs, not the counter: baseline count of `could not
retry` and `handle event failed, will retry` around the last two scoring deploys; after A4 they are 0, and
`delivery requeued at shutdown` appears only if a recompute was in flight. Rollback: the standard pin rollback.

**A5 (/readyz names dependencies; ordered shutdown).** Behaviour change; user at the keyboard.
Before merge: watchdog and the budget test use the real bounds and parse `terminationGracePeriodSeconds` from the
four Deployments; Drain differs from ConsumerDrain; `ryft_db_up` and `ryft_amqp_connected` are 0 from the first
scrape; readiness `timeoutSeconds: 3` and `terminationGracePeriodSeconds: 30` in the four Deployments, `make
k8s-validate` green; API.md Health + changelog; logcheck knows the new lines; OPERATIONS "A deploy: what the old pod
logs" with the queries above; no import in flight.
Go (one full deploy of all four services, results pasted into the PR): every old pod has "shutting down" and "shut
down" with exit 0 and dur_ms < 20000; the stuck-pod query returns no rows; no "shutdown overdue" or step with err;
smoke + `make smoke-prod` green; restarts 0; 5xx < 0.5 %; sync-to-score p95 < 120 s. No-go: any "shutdown
overdue", exit 1 on a clean SIGTERM, a pod killed with 137, 5xx > 1 %. Do not drain a production node as a test.

**A7 (5 s drain delay).** Go: the edge probe shows no 000/502/503 during one rollout, compared with a baseline
rollout recorded before A7. Rollback: pin rollback (cost: 5 s per pod).

Not verified by the reviewer: Traefik's metrics and access-log settings, the Alloy scrape interval, the CloudWatch
parse of the container log format (`data.*` prefix and field types).

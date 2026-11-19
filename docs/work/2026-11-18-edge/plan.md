# The edge: BETA_GATE off, then an external Application LB with Cloud Armor (2026-11-18)

Brief: head-architect, 2026-11-18 (inline). Two PRs from main. #211 (the per-IP limit, D240) is open; main is
merged into both branches once it lands. D241 is PR A's, D242 PR B's.

## PR A: `deploy: BETA_GATE off` (D241, amends D238)

Owner: the lead (one config line and docs). Review: code-reviewer.

- `deploy/run/config.env`: `BETA_GATE=off`, its comment says why (no codes for the beta, the user's
  decision 2026-11-18; access on the server comes with App Attest, still to be decided).
- D241; OPERATIONS → The beta gate: a note at the top (off by the user's decision, the steps kept for
  later); STATUS.
- Criteria: `make cloudrun-check` green; `bash scripts/tests/check_cloudrun_test.sh` green (it reads the
  committed `config.env` through the renderer: `off` is a valid shape); CI green; no other line moves.
- On merge: `deploy.yml` redeploys the five services with `BETA_GATE=off` on api and ingest; nothing is
  refused that warn served. Rollback: the same line back to `warn`.

## PR B: `infra: the edge, an external Application LB with Cloud Armor for api and ingest` (D242)

Owner: infra-engineer (Terraform, tftests, runbook, docs; after #211 lands also `scripts/check_cloudrun.py`,
its test, one Go table entry). Review: code-reviewer. Merged with `edge = "off"`: the merge plans nothing.

### The gate `edge` in `gates.auto.tfvars`: `off | built | live | locked`, default `off`

| Phase | What the apply does | Way back (one step) |
| --- | --- | --- |
| `off` | nothing (APIs, LB, logging, dashboard unchanged) | — |
| `built` | compute + certificatemanager on the keep list (on); global IPv4 address; 2 serverless NEGs; 2 backend services with the policy and logging 1.0; the policy (WAF preview + rate rule + default allow); URL map by host (unknown host → 301 to the site); HTTPS proxy with an SSL policy (TLS 1.2+, MODERN) and a certificate map; 2 DNS authorizations + their CNAMEs; 2 certificates; HTTP→HTTPS redirect (its URL map, proxy, forwarding rule); the sink's exclusions; 2 log-based metrics; the dashboard's edge tiles | `off`: all of it destroyed (the APIs stay on: `disable_on_destroy = false`) |
| `live` | `api.`/`ingest.` CNAME → A at the LB's IPv4 (TTL 300, already short); the mappings stay | `built`: A → CNAME |
| (deploy PR) | `ingress: internal-and-cloud-load-balancing` + `XFF_TRUSTED_HOPS=2` on api and ingest; only while `edge` is `live` or `locked` (check_cloudrun) | revert the PR |
| `locked` | the 2 domain mappings destroyed; only allowed with the LB-only ingress in `deploy/run` (check_cloudrun) | `live`: the mappings come back but their certificates can only issue once DNS points at ghs again (the one costly rollback; `locked` waits for 7 clean days) |

Order and why: the deploy PR goes right after `live` (≥ 10 min, 2 × TTL), because in `live` with hops 1 every
LB request keys the in-process limit on the LB's own address (one shared bucket per instance). The mappings go
last: until then reverting the deploy PR and `live` → `built` is the full way back with valid certificates.

### Acceptance criteria (tested)
1. `edge = "off"` (the committed value): a plan against today's state adds, changes and destroys nothing that
   main doesn't (the tftests assert no edge resource, the sink and dashboard unchanged, the keep list of 16).
2. `built`: every edge resource exists; the backend services carry the policy and `log_config` 1.0; the URL
   map routes `api.ryftlab.de` → api and `ingest.ryftlab.de` → ingest; the proxy's SSL policy is TLS 1.2+;
   the certificates use DNS authorization; the DNS records are still CNAMEs; the mappings exist.
3. The policy: the rate rule enforced (not preview), keyed on `IP`, `deny(429)`, the values below; each WAF
   rule (sqli, xss, lfi, rce, scannerdetection, protocolattack, v33-stable, sensitivity 1) in preview, at a
   priority before the rate rule; default rule allow; JSON parsing STANDARD, log level NORMAL (verbose logs
   would put body snippets in the log).
4. The sink: at `built`+ it keeps every entry except LB request logs that are neither a Cloud Armor deny nor a
   preview match, and (for the user's confirmation) api's and ingest's Cloud Run request log below 5xx (review:
   their 5xx are the only record of Cloud Run's own failures, and the 5xx alert reads them).
5. `live`: A records at the address, TTL 300, no CNAME at those names; the mappings still there.
6. `locked`: no mapping; the A records stay.
7. `edge != "off"` without `serve = true` is refused at plan time.
8. check_cloudrun (after #211; built into #213 on 2026-11-19 once #211 had merged): LB-only ingress refused
   unless `edge` is `live` or `locked`, and with `XFF_TRUSTED_HOPS=1`; `edge = "locked"` refused with ingress
   `all`; its test has a case for each; CI runs it when `gates.auto.tfvars` changes.
9. The keep list test: 16 APIs at `off`, 18 at `built`+.
10. No alert condition added (the cap-of-five test unchanged and green).
11. `terraform init -backend=false`, `fmt -check`, `validate`, `test` green; `make cloudrun-check`; CI green.

### The rate rule (to verify against Google's limits)
One `rate_based_ban` rule, key `IP`: 300 requests per 10 s (exceed `deny(429)`), ban at 6,000 per 300 s for
600 s. 300/10 s is above D53's bucket's most in 10 s (50 + 20 × 10 = 250) and above one user's per-user
ceilings on both hosts combined (api 60 + 10 × 10, ingest's sync 60 + 2 × 10: 240), below three api
instances' worth (750). The uptime checks (2 hosts × 3 locations every 5 min), the smoke (sequential) and the
tick (Pub/Sub to run.app, not through the LB) never come near it.

### IPv6
Cloud Armor documents no prefix key (*(search)*: ALL, IP, HTTP-HEADER, XFF-IP, HTTP-COOKIE, HTTP-PATH, SNI,
REGION-CODE, TLS JA3/JA4, USER-IP, ASN). So the LB gets no IPv6 address: no AAAA, so IPv6-only phones come
through their carrier's NAT64. Once `locked`, no /48 or /64 reaches D240's map.

### Risks
- Compute API on a project without an organisation creates the default network and the Compute Engine
  default SA with Editor: runbook step right after `built`'s apply (remove Editor, disable the SA, delete the
  default network and its four firewall rules).
- The 32 MiB bodies through the LB (Cloud Run's HTTP/1 cap; ingest relies on h2c): `built`'s check sends
  33 MiB → our JSON 413 and 31 MiB → not Google's 413. A Google 413 stops `live`.
- XFF behind the LB is documented, not observed: `built`'s check (hops 1, three forged entries) must show
  `xff_index 4`, `xff_entries 5`; after the deploy PR `xff_index 3`.
- The €20 stop line is below the edge's fixed cost (~$32): the user picks a new line (proposal in D242).
- Privacy: LB logs hold the client IP, URL and user agent. Only deny and preview entries are kept (14 days).
  C2 must name them. With api's and ingest's Cloud Run request log excluded below 5xx, the other place an IP
  is stored is their 5xx entries (and scoring's and notify's, which hold Pub/Sub's addresses).

### The user's decisions (2026-11-19)
The stop line €45 from `built` on (the budget's 90 % mail, no new rule; the optional €20 mail out of the
runbook); Cloud Run's request log of api and ingest excluded below 5xx; about €28 a month accepted. Recorded
in D242, OPERATIONS (16, the stop line), STATUS.

### Needs the user
- Merge PR A, then PR B (gate off). Grant the apply SA's roles (runbook). Then each phase is a one-line PR,
  the plan with the expected counts, the apply, the checks. The deploy PR after `live`; `locked` after 7
  days. WAF rules enforced one by one after 7 days without a preview match. Decide the stop line.

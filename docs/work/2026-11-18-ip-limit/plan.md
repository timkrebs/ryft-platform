# The per-IP limit in api and ingest on GCP (D53 on GCP, D240), 2026-11-18

Brief: head-architect, 2026-11-18 (inline). The user's decision: this now; an external Application Load Balancer with
Cloud Armor follows in a later PR, so the limit must keep working behind it.

## Why
D53's per-IP limit (20/s, bursts of 50, before any token) was Traefik's middleware on EKS. On Cloud Run nothing in front
does it, so requests without a token reach api and ingest unthrottled. `docs/API.md` still documents it (Errors → `429`):
the contract is unchanged, this PR makes it true again.

## Design (decided by the lead)
1. **One client-address function** (`internal/httpx`), shared by the new limit, the admin limit (D155) and the sampled
   `client address` line:
   - walk `X-Forwarded-For` from the right; skip Google's front-end ranges (as today, D222); the client is the
     `XFF_TRUSTED_HOPS`-th remaining entry (default 1: Cloud Run's front end appends the address it saw);
   - IPv6 cut to /64, IPv4-mapped read as IPv4 (as today);
   - missing header, fewer entries than the hop count, or an unparseable entry at the position: one shared
     **fallback** key (was `RemoteAddr`; on Cloud Run that is Google's front end, a shared key anyway);
   - the key handed to the limiters is an HMAC-SHA256 of the address under a per-process random key (16 bytes), so
     neither map holds a plain address.
2. **`XFF_TRUSTED_HOPS`** (1–8, default 1) in `config.Config`, a required key of `deploy/run/config.env` (=1), rendered
   into api and ingest only (D238's pattern). The LB PR sets 2 and restricts ingress to
   `internal-and-cloud-load-balancing` in the same change.
3. **`ratelimit.Capped`**: the admin's `addrLimit` moved into `internal/ratelimit` with a configurable idle time; both
   limits use it. Edge: 20/s, burst 50, 65,536 keys per instance, idle 1 min. Admin: unchanged (10/s, 20, 4,096, 10 min).
   Full: known keys keep their bucket, new keys share one overflow bucket (fails closed for newcomers).
4. **Where:** `internal/app`, Edge services only: Instrument → Recover → SecurityHeaders → **IP limit** → start gate.
5. **Exempt:** the health paths (`httpx.IsLive`/`IsReady`: `/health/live`, `/health/ready`, compose's `/healthz`,
   `/readyz`), `POST /internal/tick` (Pub/Sub push of the tick, the only push api and ingest take), and on api a request
   to `/v1/admin/*` bearing `ADMIN_API_KEY` (constant time, keeps D222's break-glass guarantee).
6. **Answer:** `429`, body `Too Many Requests` (text/plain; charset=utf-8, no JSON), `Retry-After` = the wait rounded up
   (min 1), `X-Retry-In` = the wait as a Go duration (Traefik's format, e.g. `37.5ms`). No CORS headers.
7. **Privacy (D222):** no address in a log line, label, attribute or store; `ryft_rate_limited_total{class="ip"}` and the
   access line's `code` `ip_limit` are the only traces.

## Tickets
| # | Owner | What | Acceptance |
|---|---|---|---|
| T1 | backend-engineer | 1–7 in `backend/`, `scripts/cloudrun_render.py`, `scripts/check_cloudrun.py`, their tests, `deploy/run/{config.env,api.yaml,ingest.yaml}`, `docs/ARCHITECTURE.md` pkgmap counts | unit tests: bucket, eviction, cap/overflow, header parsing incl. spoofed left entries and hop counts, fallback, exemptions, the 429's exact shape, no address in logs, the push-route guard; `go vet`, `go test`, `make lint`, `scripts/pkgmap.sh --check`, `make cloudrun-check` green |
| T2 | lead | `docs/API.md` changelog + Errors/CORS wording, D240, OPERATIONS (burst test, Logs Explorer check, LB note), OBSERVABILITY row, STATUS | `go test ./...` still green (API.md tests) |
| T3 | code-reviewer | one review round on the diff | findings in `review.md`, blockers fixed and re-checked |
| T4 | lead | PR, gitleaks, CI green | `gh pr checks` all green |

## Risks
- A hop count of 2 while ingress is still `all`: a direct caller's forged entry becomes the key. Hence "2 only with the
  ingress change" in D240 and the runbook.
- A Google hop outside the known ranges at hops=1 puts every client into one bucket (fails closed, 20/s shared). Step 9's
  `xff_index` check catches it; the burst test shows it as 429s far below 20/s.
- A distributed flood (> 65,536 new /64s a minute per instance) moves newcomers into the overflow bucket: testers who were
  idle may get 429 while it lasts. Cloud Armor (next PR) is the answer to that.
- Smoke and tests without `X-Forwarded-For` share the fallback bucket (50, then 20/s): smoke is sequential and small.

## Rollback
Revert the PR (a merge to main redeploys). No migration, no infra.

## Needs the user
After the deploy: the burst test and the Logs Explorer check (OPERATIONS → GCP → The per-IP limit).

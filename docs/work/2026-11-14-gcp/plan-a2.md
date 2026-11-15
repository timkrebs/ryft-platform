# Plan A2: `backend: the Cloud Run runtime` (2026-11-15)

The rest of ticket A (`design.md` §10 row A), after A1 (#169, `ecc7401`). After A2 merges the user runs runbook step 8, the
first deploy, so A2 leaves main deployable: no `FILL_ME_IN`, `make cloudrun-check` green, images that match `ticket-b.md`.
Branch `backend/cloudrun` from `origin/main`. Budget: 3 builders, one review round (code + security), at most one fix agent.

## Tickets (one writer per file set)

| # | Owner | Items | Files | Acceptance |
|---|---|---|---|---|
| B1 | backend-engineer | 1 h2c, 2 health, 5 logs, 6 ClientIP, 7 shutdown, 8 Supabase JWKS + jobs | `internal/httpx/**`, `internal/logx/**`, `internal/app/**`, `cmd/{api,ingest,scoring,notify}/**`, `internal/auth/supabase*.go`, `internal/auth/doc.go`, `internal/config/config.go`, `config_test.go`, `config/doc.go` | below |
| B2 | backend-engineer | 4 OTLP, 9 alerts and docs | `internal/metrics/**` (incl. the stub `otlp.go`), `internal/config/otlp.go` (+ test), `deploy/grafana/**`, `docs/OBSERVABILITY.md` | below |
| B3 | database-engineer | 3 database, 10 images | `internal/db/**`, `internal/store/*_test.go` (new pooler tests only), `backend/Dockerfile`, `backend/certs/**`, `docker-compose.yml`, `.github/workflows/ci.yml`, `Makefile`, `scripts/check_session_state.sh` + `scripts/tests/check_session_state_test.sh` | below |
| L | lead | 11 config, 12 docs | `deploy/run/config.env`, `docs/{DECISIONS,API,SUPABASE,OPERATIONS,STATUS}.md`, this folder | `make cloudrun-check`, `make check-api-doc` |
| R | code-reviewer ∥ security-privacy-reviewer | one round on the diff | read-only → `review-a2.md` | blockers fixed (lead or one fix agent); CI is the re-check |

## Interfaces fixed before the build

**OTLP (B2 implements, B1 wires).** The lead committed a compiling stub; the signatures stay:

```go
// internal/config/otlp.go
type OTLP struct{ Endpoint, User, Token string } // OTLP_ENDPOINT, OTLP_USER, OTLP_TOKEN; Endpoint and User both "" = no push
func LoadOTLP() (OTLP, error)                    // refuses one of Endpoint/User without the other, a non-https endpoint, …

// internal/metrics/otlp.go
type OTLPOptions struct{ Endpoint, User, Token, Service, InstanceID string; Gatherer prometheus.Gatherer; Client *http.Client; Log *slog.Logger }
func NewOTLP(o OTLPOptions) (*OTLP, error) // Endpoint "" → nil, nil
func (p *OTLP) Push(ctx context.Context) error // nil receiver → nil; honours ctx (the caller bounds it)
```

B1 calls `config.LoadOTLP` at start (an error ends the process like any config error), builds the pusher, hands
`p.Push` to the tick as `Finally` (all four services) and pushes once in the shutdown, after the HTTP step, inside the
re-pinned budget.

**Health (B1).** `GET /health/live` = `/healthz`'s handler, `GET /health/ready` = `/readyz`'s, on all four services.
With `K_SERVICE` set, both ready paths answer `503 {"error":"not ready"}` (no `Retry-After`), log the cause at warn, and
cache the result (ok or not) for 5 s. Without `K_SERVICE`, `/readyz` and `/health/ready` keep today's body. The
service listens before it opens the pool: `/health/live` answers 200 at once, ready paths 503 until the start is done.

**Database (B3).** On Cloud Run (`K_SERVICE` or `CLOUD_RUN_JOB` set), `db.Open` refuses a `DATABASE_URL` with a password,
or without `sslmode=verify-full` and `sslrootcert`; a service (not migrate) also needs `default_query_exec_mode=exec`
and port 6543. The CA is `/etc/ryft/supabase-ca.crt` in every image.

## Risks
- B1 is the largest ticket (four mains, two of them without `internal/app`). It is the critical path.
- Nobody can run Podman, so `make smoke` and `make alerts-check` run only in CI; plan on one red round.
- The four image digests: two from head-architect, two read off main's CI build log (BuildKit's `resolve` lines). No
  `docker` here, so a pull by digest in CI is the check.

## Rollback
Nothing deploys on merge while `GCP_DEPLOY` is off; with it on, `gcloud run services update-traffic` (OPERATIONS → GCP).
Reverting the PR restores the A1 runtime.

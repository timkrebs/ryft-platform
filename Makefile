SERVICES := ingest scoring api notify migrate
# The golangci-lint that CI's `lint` job pins (.github/workflows/ci.yml); `make lint` warns when yours differs.
GOLANGCI_LINT_VERSION := v2.15.0

# Docker if it is installed, otherwise Podman (its `compose` runs the same file; D1).
CONTAINER ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || echo podman)
COMPOSE   ?= $(CONTAINER) compose

.PHONY: test check-api-doc test-scripts build up down logs smoke smoke-hook smoke-supabase smoke-prod pooler-check session-state-check cloudrun-check check-swift web-install web-test web-build web-e2e web-hosting web-tokens hooks coverage load lint pkgmap-check topology-check

test:
	cd backend && go vet ./... && go test ./...

# The binaries, for a look on this machine. The images are deploy.yml's (built from backend/Dockerfile
# on the runner, scanned, pushed to Artifact Registry); compose builds its own (`make up`).
build:
	cd backend && for s in $(SERVICES); do CGO_ENABLED=0 go build -o bin/$$s ./cmd/$$s; done

# Local dev via docker compose: Postgres, the Pub/Sub emulator with the generated topology
# (deploy/pubsub-emulator/init.sh), a tick every TICK_EVERY seconds (default 60) and the services.
up:
	$(COMPOSE) up --build -d

down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f ingest scoring api

# Push a synthetic night + workout through the stack and read the score back. In apikey mode it then
# connects the fake provider as a throwaway account (start, callback, confirm, a signed push and its
# duplicate, the night scored, User.sources, disconnect deletes it; D257). CONNECT_SMOKE=off skips that
# pass; CONNECT_COVERAGE=on adds its coverage checks (on by default from the PR connect/scoring).
smoke:
	./scripts/smoke.sh

# The same against a stack running AUTH_MODE=supabase: signs in a test user with
# the GoTrue password grant. Skips unless SUPABASE_URL, SUPABASE_ANON_KEY,
# SMOKE_EMAIL and SMOKE_PASSWORD are set. It writes synthetic days into that user,
# so use the dedicated smoke account. API_URL and INGEST_URL name the stack: Cloud
# Run's hosts (runbook step 9: https://api.ryftlab.de, https://ingest.ryftlab.de,
# which proves pgx's exec mode through Supavisor on 6543, design §4) or a local one
# (the default, compose's ports). Plain http is refused for anything but localhost:
# the user's access token would cross the network in clear.
smoke-supabase:
	@if [ -z "$$SUPABASE_URL" ] || [ -z "$$SUPABASE_ANON_KEY" ] || [ -z "$$SMOKE_EMAIL" ] || [ -z "$$SMOKE_PASSWORD" ]; then \
	  echo "smoke-supabase: skipped (set SUPABASE_URL, SUPABASE_ANON_KEY, SMOKE_EMAIL, SMOKE_PASSWORD; API_URL/INGEST_URL point at the stack)"; \
	  exit 0; \
	fi; \
	for u in "$${API_URL:-http://localhost:8082}" "$${INGEST_URL:-http://localhost:8081}" "$$SUPABASE_URL"; do \
	  case "$$u" in https://?*|http://localhost:*|http://127.0.0.1:*) ;; \
	    *) echo "smoke-supabase: $$u is neither https:// nor local" >&2; exit 2 ;; esac; \
	done; \
	echo "smoke-supabase: api $${API_URL:-http://localhost:8082}, ingest $${INGEST_URL:-http://localhost:8081}"; \
	AUTH_MODE=supabase ./scripts/smoke.sh; \
	if [ -n "$$SUPABASE_SECRET_KEY" ]; then ./scripts/check_accounts.py; \
	else echo "accounts: skipped (set SUPABASE_SECRET_KEY to test sign-up, isolation and deletion with throwaway users)"; fi

# Runbook step 7 (docs/OPERATIONS.md → GCP), before anything is deployed: pgx's exec mode (unnamed
# statements, as in the services' DATABASE_URL) through Supavisor's transaction pooler on 6543, read-only.
# TestPooler (backend/internal/store/pooler_test.go) logs in once as postgres.<ref> with verify-full
# against backend/certs/supabase-ca.crt, on one connection, and runs the store's parameter types, a
# batch, SET LOCAL and pg_advisory_xact_lock in a READ ONLY transaction that it rolls back. The ref and
# the host come from deploy/run/config.env, the owner's password only from PGPASSWORD in the environment.
# One attempt: a failed login feeds Supavisor's circuit breaker for the whole role, so never rerun it in
# a loop. CI's pooler job runs the same test against PgBouncer. Supabase's extra_float_digits is 0 and the
# owner has no role setting, so the test reports it and passes; the service roles get 1 from migration
# 0035, which runbook step 9's login as ryft_api checks (D228).
pooler-check:
	@[ -n "$$PGPASSWORD" ] || { echo "pooler-check: export PGPASSWORD first, the owner's (db-password-owner; docs/OPERATIONS.md → GCP, step 7)" >&2; exit 2; }
	@ref=$$(sed -n 's/^SUPABASE_PROJECT_REF=//p' deploy/run/config.env); host=$$(sed -n 's/^SUPABASE_POOLER_HOST=//p' deploy/run/config.env); \
	printf '%s\n' "$$ref" | grep -Eqx '[a-z]{20}' || { echo "pooler-check: SUPABASE_PROJECT_REF in deploy/run/config.env is not one value of 20 lower-case letters" >&2; exit 2; }; \
	printf '%s\n' "$$host" | grep -Eqx 'aws-[0-9]+-eu-central-1\.pooler\.supabase\.com' || { echo "pooler-check: SUPABASE_POOLER_HOST in deploy/run/config.env is not one aws-<n>-eu-central-1.pooler.supabase.com" >&2; exit 2; }; \
	echo "pooler-check: postgres.$$ref at $$host:6543, one connection, read-only"; \
	cd backend && RYFT_POOLER_URL="postgresql://postgres.$$ref@$$host:6543/postgres?sslmode=verify-full&sslrootcert=$(CURDIR)/backend/certs/supabase-ca.crt&default_query_exec_mode=exec" \
	  go test -count=1 -run '^TestPooler$$' -v ./internal/store/

# No session state in the services' Go and SQL (scripts/check_session_state.sh: SET without LOCAL,
# LISTEN, temporary tables, session advisory locks, named prepared statements, WITH HOLD cursors),
# which Supavisor's transaction pooler would leave on another client's server connection. CI's lint job.
session-state-check:
	bash scripts/check_session_state.sh

# The Go smoke (internal/smoke, `/app smoke` in the migrate image) against the compose stack, as
# CI's smoke job runs it after `make smoke`.
smoke-hook:
	$(COMPOSE) run --rm --no-deps \
	  -e SMOKE_API_URL=http://api:8080 -e SMOKE_INGEST_URL=http://ingest:8080 \
	  -e SMOKE_HEALTH_URLS=http://api:8080/readyz,http://ingest:8080/readyz,http://scoring:8080/readyz,http://notify:8080/readyz \
	  -e SMOKE_TOKEN=$${API_KEY:-dev-api-key-change-me}:$$(python3 -c 'import uuid; print(uuid.uuid4())') \
	  migrate smoke

# The operator scripts (scripts/tests): fakes of gcloud, gh and kubectl, and the compose api when it
# is up. Every test runs, also after one has failed; the target fails at the end and names each
# failed test, so one CI round shows all of them.
test-scripts:
	@failed=; n=0; for t in scripts/tests/*_test.sh; do n=$$((n + 1)); bash "$$t" || failed="$$failed $$t"; done; \
	if [ -n "$$failed" ]; then \
	  echo "✗ test-scripts: $$(echo $$failed | wc -w | tr -d ' ') of $$n failed:" >&2; \
	  for t in $$failed; do echo "  $$t" >&2; [ "$${GITHUB_ACTIONS:-}" != true ] || echo "::error title=test-scripts::$$t failed"; done; \
	  exit 1; \
	fi; \
	echo "✓ test-scripts: all $$n passed"

# After a production deploy (docs/OPERATIONS.md → GCP → Deploy, roll back, rotate): the edge, the
# dedicated smoke user's sign-in, /v1/me, one tiny sync, the day read back.
smoke-prod:
	./scripts/smoke_prod.sh

# deploy/run, what deploy.yml applies to Cloud Run (CI's manifests job): config.env lints, every template
# renders with fake values, and the result holds the limits, ingress, accounts, h2c port, probes, env,
# secret pins and accessors of the design and ticket-b.md (scripts/check_cloudrun.py). Needs yq (mikefarah v4).
# CLOUDRUN_BASE=<path>: the PR base's config.env (CI's manifests job writes it to .ci/), for App Attest's
# order off -> warn -> on (D245); without it that rule is skipped.
CLOUDRUN_BASE ?=
cloudrun-check:
	python3 -I scripts/check_cloudrun.py $(if $(CLOUDRUN_BASE),--base-config "$(CLOUDRUN_BASE)")

# After smoke, on macOS: decode every endpoint with the Swift mirror in docs/API.md
# and with the app's current models (../app). See scripts/check_swift.sh.
check-swift:
	./scripts/check_swift.sh

# Per-package Go coverage and the internal/ total, as CI prints them (scripts/coverage.sh). The
# profile and the numbers go to $(COVERDIR), a temp dir by default; tests that need Postgres or the
# Pub/Sub emulator count only with RYFT_TEST_POSTGRES_URL / RYFT_TEST_PUBSUB_EMULATOR_HOST set, as in CI.
coverage:
	@set -e; d=$${COVERDIR:-$$(mktemp -d)}; mkdir -p "$$d"; \
	(cd backend && go test -race -count=1 -coverprofile="$$d/coverage.out" ./... > "$$d/test.log" 2>&1) \
	  || { echo "go test failed, tail of $$d/test.log:" >&2; tail -40 "$$d/test.log" >&2; exit 1; }; \
	env -u GITHUB_STEP_SUMMARY ./scripts/coverage.sh report "$$d/coverage.out" "$$d"; echo "files: $$d"

# Load baseline against the LOCAL compose stack (make up first): 50 synthetic users sync a full day, then read
# 30 days, one warm-up and 3 measured runs under k6 (a container, pinned by digest). Prints a table, writes
# scripts/load/out/ (git-ignored) and exits 1 above 1 % errors. Targets other than localhost are refused
# unless LOAD_ALLOW_REMOTE=1; never aim it at production (docs/OBSERVABILITY.md -> Baselines).
load:
	CONTAINER="$(CONTAINER)" COMPOSE="$(COMPOSE)" ./scripts/load.sh

# What CI's `lint` job runs: golangci-lint with the repo config (.golangci.yml) from backend/, the package
# map check and the session-state check. Then the decision references (D165), which CI runs in its `bytes` job
# (warnings only: scripts/decision_refs.sh exits 0 unless it cannot work).
# The linter is not installed by the Makefile: CI pins GOLANGCI_LINT_VERSION, and another version can report
# different findings, so a mismatch is said out loud before the run.
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "lint: golangci-lint is not installed; CI pins $(GOLANGCI_LINT_VERSION) (https://golangci-lint.run/welcome/install/)" >&2; exit 1; }
	@have=$$(golangci-lint version 2>&1 | sed -n 's/.*version v\{0,1\}\([0-9][0-9.]*\).*/\1/p' | head -n 1); \
	if [ "$$have" != "$(patsubst v%,%,$(GOLANGCI_LINT_VERSION))" ]; then \
	  echo "lint: WARNING: golangci-lint $${have:-of unknown version} found, CI pins $(GOLANGCI_LINT_VERSION); findings can differ from CI's" >&2; \
	fi
	cd backend && golangci-lint run
	./scripts/pkgmap.sh --check
	bash scripts/check_session_state.sh
	./scripts/decision_refs.sh

# After editing docs/API.md (D167): the tests that read it (examples, Errors tables, quoted messages).
# docs/ is outside the Go module, so the test cache does not see an edit; -count=1 re-runs them.
check-api-doc:
	cd backend && go test -count=1 -run 'APIDoc|DocChecker' ./internal/model/ ./internal/httpx/ ./internal/gate/

# docs/ARCHITECTURE.md -> Package map is generated; fails (1) when it is stale, `scripts/pkgmap.sh` rewrites it.
pkgmap-check:
	./scripts/pkgmap.sh --check

# Regenerate the scoring golden files (backend/internal/scoring/testdata/golden) after a
# ModelVersion bump or a generator change; GOLDEN_MODE=additive accepts new fields only, and
# GOLDEN_MODE=init makes the first set. It starts from the committed manifest and a local database
# (GOLDEN_ALLOW_REMOTE_DB=1 for another host); the test refuses anything else (docs/SCORING.md →
# Model version, D149; scripts/golden_guard.sh is the PR-side check, run by CI's golden-guard job).
.PHONY: golden-update
golden-update:
	@: "$${RYFT_TEST_POSTGRES_URL:?a throwaway Postgres server, e.g. postgres://postgres:<password>@localhost:5432/postgres}"
	cd backend && RYFT_GOLDEN_UPDATE=$(or $(GOLDEN_MODE),1) go test ./internal/scoring/ -run '^TestGoldenScores$$' -count=1 -v

# The Pub/Sub topology's generated files, infra/gcp/pubsub.auto.tfvars.json (Terraform's input) and
# deploy/pubsub-emulator/init.sh (compose's emulator), are what backend/internal/events/pubsub/topology.go
# writes; exit 1 names each one that drifted (`go run ./cmd/pubsub-topology` in backend/ rewrites both).
# CI's `topology` job.
topology-check:
	cd backend && go run ./cmd/pubsub-topology -check

# --- web/ (ryftlab.de, D107): one place for the site's checks; `make hooks` once per clone ---
# The drift checks and some tests read the app from ../app (RYFT_APP_DIR overrides it: absolute,
# or relative to web/).

web-install:
	cd web && npm ci && npx playwright install chromium

# Typecheck, ESLint + Prettier + brand, Vitest. Add REQUIRE_SIBLINGS=1 to make
# a missing ../app an error instead of a skip.
web-test:
	cd web && npm run typecheck && npm run lint && npm test

# The static export in web/out (postbuild: CSP hashes, brand check of the output).
web-build:
	cd web && npm run build

# The Firebase Hosting emulator (firebase-tools pinned in web/.firebase-tools-version, no login, no
# Podman or Docker) on the build, and against it Playwright (routes, headers, axe), check-hosting.mjs,
# links and Lighthouse.
web-e2e: web-build
	cd web && npm run e2e:hosting

# The same emulator until Ctrl-C, at http://localhost:8080.
.PHONY: web-hosting
web-hosting: web-build
	cd web && npm run hosting

# Regenerate src/styles/tokens.* from the app (../app/Ryft).
web-tokens:
	cd web && npm run tokens

# The brand guard on staged web/ files (pre-commit) and on every commit message (commit-msg).
hooks:
	git config core.hooksPath .githooks
	@echo "✓ git hooks: .githooks (brand check on staged web/ files and on commit messages)"

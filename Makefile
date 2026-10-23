REGISTRY ?= ghcr.io/timkrebs/ryft
TAG      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
SERVICES := ingest scoring api notify migrate

# Docker if it is installed, otherwise Podman (its `compose` runs the same file).
CONTAINER ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || echo podman)
COMPOSE   ?= $(CONTAINER) compose

.PHONY: test test-scripts build images push up down logs smoke smoke-hook smoke-supabase smoke-prod alerts-check alerts-apply synthetics-check synthetics-apply check-swift k8s-apply-eks k8s-check-auth k8s-check-images k8s-check-registry k8s-rabbitmq k8s-apply-local k8s-delete-local k8s-validate monitoring-apply tf-migrate-hcp web-install web-test web-build web-e2e web-image web-serve web-serve-stop web-tokens hooks

test:
	cd backend && go vet ./... && go test ./...

build:
	cd backend && for s in $(SERVICES); do CGO_ENABLED=0 go build -o bin/$$s ./cmd/$$s; done

images:
	for s in $(SERVICES); do \
	  $(CONTAINER) build -t $(REGISTRY)/$$s:$(TAG) -t $(REGISTRY)/$$s:latest --build-arg SERVICE=$$s backend; \
	done

# Only the commit tag: nothing deploys :latest (Argo CD pins a SHA, D96); the local
# :latest from `make images` is for the self-hosted overlay you build yourself.
push: images
	for s in $(SERVICES); do $(CONTAINER) push $(REGISTRY)/$$s:$(TAG); done

# Local dev: Postgres + RabbitMQ + the services via docker compose.
up:
	$(COMPOSE) up --build -d

down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f ingest scoring api

# Push a synthetic night + workout through the stack and read the score back.
smoke:
	./scripts/smoke.sh

# The same against a stack running AUTH_MODE=supabase (e.g. EKS through
# port-forwards): signs in a test user with the GoTrue password grant. Skips
# unless SUPABASE_URL, SUPABASE_ANON_KEY, SMOKE_EMAIL and SMOKE_PASSWORD are set.
# It writes synthetic days into that user, so use a dedicated test account.
smoke-supabase:
	@if [ -z "$$SUPABASE_URL" ] || [ -z "$$SUPABASE_ANON_KEY" ] || [ -z "$$SMOKE_EMAIL" ] || [ -z "$$SMOKE_PASSWORD" ]; then \
	  echo "smoke-supabase: skipped (set SUPABASE_URL, SUPABASE_ANON_KEY, SMOKE_EMAIL, SMOKE_PASSWORD; API_URL/INGEST_URL point at the stack)"; \
	  exit 0; \
	fi; \
	AUTH_MODE=supabase ./scripts/smoke.sh; \
	if [ -n "$$SUPABASE_SECRET_KEY" ]; then ./scripts/check_accounts.py; \
	else echo "accounts: skipped (set SUPABASE_SECRET_KEY to test sign-up, isolation and deletion with throwaway users)"; fi

# Argo CD's PostSync smoke (internal/smoke, `/app smoke` in the migrate image) against the
# compose stack, as CI runs it after `make smoke`.
smoke-hook:
	$(COMPOSE) run --rm --no-deps \
	  -e SMOKE_API_URL=http://api:8080 -e SMOKE_INGEST_URL=http://ingest:8080 \
	  -e SMOKE_HEALTH_URLS=http://api:8080/readyz,http://ingest:8080/readyz,http://scoring:8080/readyz,http://notify:8080/readyz \
	  -e SMOKE_TOKEN=$${API_KEY:-dev-api-key-change-me}:$$(python3 -c 'import uuid; print(uuid.uuid4())') \
	  migrate smoke

# The operator scripts (scripts/tests): a fake kubectl, and the compose api when it is up.
test-scripts:
	@set -e; for t in scripts/tests/*_test.sh; do bash "$$t"; done

# After a production deploy (docs/OPERATIONS.md → Deploy and roll back): the edge, the
# dedicated smoke user's sign-in, /v1/me, one tiny sync, the day read back.
smoke-prod:
	./scripts/smoke_prod.sh

# After smoke, on macOS: decode every endpoint with the Swift mirror in docs/API.md
# and with the app's current models (../app). See scripts/check_swift.sh.
check-swift:
	./scripts/check_swift.sh

# One namespace per service (docs/ARCHITECTURE.md → Namespaces).
NAMESPACES := migrate ingest scoring api notify smoke
LOCAL_SECRETS := deploy/k8s/overlays/local-postgres/secrets

# BREAK-GLASS ONLY. Argo CD deploys ryft-prod-euc1 from git (docs/OPERATIONS.md →
# Deploy, D95): a merged pin rolls out by itself, and self-heal reverts within minutes
# whatever is applied here that git does not hold. Use this when Argo CD itself is
# down, after pausing its auto-sync (OPERATIONS.md → Pause). Order as before Argo:
# the Vault Secrets Operator objects first, so each namespace has its Secret before
# the pods start; then RabbitMQ (k8s-rabbitmq) and the migrate Job (label
# ryft.dev/stage=first), and the services only once migrate has set their role
# passwords; the PostSync smoke Job (label ryft.dev/hook) is left out. The finished
# migrate Job is deleted first because a Job's pod template cannot change.
k8s-apply-eks: k8s-check-images k8s-check-registry k8s-check-auth
	@echo "! break-glass: Argo CD reverts anything here that git does not hold; pause its auto-sync first (docs/OPERATIONS.md → Deploy)."
	for s in $(NAMESPACES); do kubectl apply -k deploy/k8s/overlays/vault-namespaces/$$s || exit 1; done
	for s in $(NAMESPACES); do kubectl -n ryft-$$s wait --for=create secret/ryft-secrets --timeout=120s || exit 1; done
	kubectl apply -k deploy/k8s/overlays/vault-namespaces/rabbitmq
	for u in ingest scoring api notify; do kubectl -n ryft-rabbitmq wait --for=create secret/amqp-user-$$u --timeout=120s || exit 1; done
	@$(MAKE) --no-print-directory k8s-rabbitmq RABBITMQ=deploy/rabbitmq
	kubectl -n ryft-migrate delete job migrate --ignore-not-found
	kubectl apply -k deploy/k8s/overlays/eks -l ryft.dev/stage=first
	kubectl -n ryft-migrate wait --for=condition=complete job/migrate --timeout=10m
	kubectl apply -k deploy/k8s/overlays/eks -l 'ryft.dev/hook!=postsync'
	for s in ingest api scoring notify; do kubectl -n ryft-$$s rollout status deploy/$$s --timeout=10m; done

# Grafana Cloud collector (docs/OBSERVABILITY.md → Grafana Cloud), after infra/vault:
# its Fleet Management token is synced from Vault into namespace monitoring first.
K8S_MONITORING_VERSION := 4.5.3
monitoring-apply: k8s-check-auth
	kubectl apply -k deploy/k8s/overlays/vault-namespaces/monitoring
	kubectl -n monitoring wait --for=create secret/ryft-secrets --timeout=120s
	helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1 || true
	helm repo update grafana >/dev/null
	helm upgrade --install grafana-cloud grafana/k8s-monitoring --version $(K8S_MONITORING_VERSION) \
	  --namespace monitoring --values deploy/monitoring/k8s-monitoring.yaml --wait --timeout 10m

# Move every Terraform root's state into its HCP Terraform workspace, once
# (docs/OPERATIONS.md → Terraform state). Needs `terraform login` and the four
# workspaces, each with execution mode Local and Terraform 1.16.6. The source is
# each root's last full local state: terraform.tfstate, or terraform.tfstate.backup
# where the S3 attempt left terraform.tfstate empty. It is copied to
# ~/.ryft-tfstate-rescue/<time>/ first. A workspace that already holds state is left
# alone. Terraform refuses -reconfigure with a cloud block, so the root's backend cache
# (.terraform/terraform.tfstate: the S3 settings, no resources) is moved next to the
# copy instead: init then starts fresh and offers no migration from S3. After the
# push the workspace must hold as many resources as the local state.
TF_ORG   := tim-krebs-org
TF_ROOTS := infra/aws:ryft-prod-euc1-platform infra/aws/dns:ryft-prod-euc1-dns \
            infra/aws/kubernetes:ryft-prod-euc1-kubernetes infra/vault:ryft-prod-euc1-vault
tf-migrate-hcp:
	@set -e; \
	token=$$(jq -r '.credentials["app.terraform.io"].token // empty' $$HOME/.terraform.d/credentials.tfrc.json 2>/dev/null); \
	[ -n "$$token" ] || { echo "✗ not logged in to HCP Terraform: terraform login"; exit 1; }; \
	rescue=$$HOME/.ryft-tfstate-rescue/$$(date +%Y%m%dT%H%M%S); mkdir -p "$$rescue"; chmod 700 "$$rescue"; \
	for pair in $(TF_ROOTS); do \
	  r=$${pair%%:*}; ws=$${pair#*:}; \
	  attrs=$$(curl -fsS -H "Authorization: Bearer $$token" \
	    https://app.terraform.io/api/v2/organizations/$(TF_ORG)/workspaces/$$ws) || { echo "✗ workspace $$ws not found in $(TF_ORG)"; exit 1; }; \
	  mode=$$(echo "$$attrs" | jq -r '.data.attributes["execution-mode"]'); \
	  [ "$$mode" = local ] || { echo "✗ $$ws: execution mode is $$mode; set it to Local (Settings → General)"; exit 1; }; \
	  if [ -n "$$(echo "$$attrs" | jq -r '.data.relationships["current-state-version"].data.id // empty')" ]; then \
	    echo "• $$ws already holds a state: left as it is"; continue; fi; \
	  src=$$r/terraform.tfstate; [ -s $$src ] || src=$$r/terraform.tfstate.backup; \
	  [ -s $$src ] || { echo "✗ $$r: no local state to move"; exit 1; }; \
	  cp $$src "$$rescue/$$ws.tfstate"; \
	  want=$$(jq '[.resources[]?.instances[]?] | length' $$src); \
	  cache=$$r/.terraform/terraform.tfstate; \
	  if [ -f $$cache ] && [ "$$(jq -r '.backend.type // empty' $$cache)" != cloud ]; then \
	    mv $$cache "$$rescue/$$ws.backend-cache.json"; fi; \
	  (cd $$r && terraform init -input=false >/dev/null); \
	  (cd $$r && terraform state push "$$rescue/$$ws.tfstate"); \
	  got=$$(cd $$r && terraform state list | wc -l | tr -d ' '); \
	  [ "$$got" -ge "$$want" ] || { echo "✗ $$ws: $$got resources after the push, $$want expected"; exit 1; }; \
	  echo "✓ $$r → $$ws: $$got resources"; \
	done; \
	echo "✓ every state is in HCP Terraform; copies in $$rescue (they hold secrets: delete them once every root plans cleanly)"

# Self-hosted (in-cluster Postgres, API key): generates the per-namespace secrets once.
# Self-hosted: cert-manager must be installed. The internal CA (the same chart template
# as on EKS), the RabbitMQ operators, then everything else with one broker node.
k8s-apply-local:
	./scripts/local-secrets.sh 2>/dev/null || [ -d $(LOCAL_SECRETS) ]
	helm template ryft-issuers infra/aws/kubernetes/charts/cluster-issuers --namespace cert-manager \
	  --show-only templates/internal-ca.yaml | kubectl apply -f -
	kubectl -n cert-manager wait --for=condition=Ready certificate/ryft-internal-ca --timeout=2m
	kubectl -n ryft-migrate delete job migrate --ignore-not-found
	@$(MAKE) --no-print-directory k8s-rabbitmq RABBITMQ=deploy/k8s/overlays/local-postgres

# The RabbitMQ operators (their CRDs and webhooks first), then $(RABBITMQ) (the broker,
# its topology; for local-postgres everything), then wait for the broker. Server-side
# apply (the CRDs are too large for the last-applied annotation), taking over fields
# Argo CD owns: this is break-glass or a cluster without Argo CD.
k8s-rabbitmq:
	kubectl apply --server-side --force-conflicts -k deploy/rabbitmq/operators
	for d in rabbitmq-cluster-operator messaging-topology-operator; do kubectl -n rabbitmq-system rollout status deploy/$$d --timeout=5m || exit 1; done
	kubectl apply --server-side --force-conflicts -k $(RABBITMQ)
	kubectl -n ryft-rabbitmq wait --for=condition=AllReplicasReady rabbitmqcluster/rabbitmq --timeout=15m

k8s-delete-local:
	kubectl delete -k deploy/k8s/overlays/local-postgres

# Render every overlay offline and validate it (no cluster needed): each service's
# namespace, EKS, local-postgres (with throwaway secrets if none exist), the
# Vault Secrets Operator manifests infra/vault generated (if any) and the golden
# copies its tests pin, the RabbitMQ manifests and Argo CD's. CustomResourceDefinitions
# are skipped (kubeconform has no schema for them): the only ones are the vendored
# operators', which SHA256SUMS pins.
KUBECONFORM_CRDS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
# Alert rules as code (deploy/grafana/alerts, D85): promtool checks the rules and runs
# their unit tests; mimirtool loads them into Grafana Cloud's ruler. Containers, so
# neither tool has to be installed.
# Pinned by digest (the index, all platforms); the tags are prom/prometheus:v3.5.0 and
# grafana/mimirtool:2.16.0. alerts-apply hands the rules token to mimirtool.
PROMTOOL_IMAGE := docker.io/prom/prometheus@sha256:63805ebb8d2b3920190daf1cb14a60871b16fd38bed42b857a3182bc621f4996
MIMIRTOOL_IMAGE := docker.io/grafana/mimirtool@sha256:f172fbe57ff63694a9b284f745e271deeb8eb782b97a3e3de9abaf07d45d276c
alerts-check:
	@set -e; d=$$(mktemp -d); trap 'rm -rf "$$d"' EXIT; chmod 755 "$$d"; \
	sed '/^namespace:/d' deploy/grafana/alerts/ryft.yaml > "$$d/ryft.rules.yaml"; \
	cp deploy/grafana/alerts/ryft.test.yaml "$$d/"; chmod a+r "$$d"/*; \
	$(CONTAINER) run --rm -v "$$d":/rules:ro --entrypoint promtool $(PROMTOOL_IMAGE) check rules /rules/ryft.rules.yaml; \
	$(CONTAINER) run --rm -v "$$d":/rules:ro -w /rules --entrypoint promtool $(PROMTOOL_IMAGE) test rules /rules/ryft.test.yaml; \
	$(CONTAINER) run --rm -v $(CURDIR)/deploy/grafana/alerts:/alerts:ro $(MIMIRTOOL_IMAGE) rules check /alerts/ryft.yaml

alerts-apply: alerts-check
	@: "$${GRAFANA_PROM_URL:?e.g. https://prometheus-prod-65-prod-eu-west-2.grafana.net}" \
	   "$${GRAFANA_PROM_USER:?the Prometheus instance id of the Grafana stack, a number}" \
	   "$${GRAFANA_RULES_TOKEN:?an access policy token with rules:read and rules:write}"
	MIMIR_API_KEY="$$GRAFANA_RULES_TOKEN" $(CONTAINER) run --rm -v $(CURDIR)/deploy/grafana/alerts:/alerts:ro \
	  -e MIMIR_ADDRESS="$$GRAFANA_PROM_URL" -e MIMIR_TENANT_ID="$$GRAFANA_PROM_USER" -e MIMIR_API_KEY \
	  $(MIMIRTOOL_IMAGE) rules load /alerts/ryft.yaml

# External checks of ryftapp.de in Grafana Cloud Synthetic Monitoring (deploy/grafana/synthetics).
# synthetics-check validates the definitions offline; synthetics-apply upserts them by job name
# through the SM API (SM_API_URL, SM_ACCESS_TOKEN; docs/OPERATIONS.md -> Monitoring).
synthetics-check:
	@./scripts/synthetics.sh check

synthetics-apply:
	@./scripts/synthetics.sh apply

k8s-validate:
	@set -e; out=$$(mktemp); gen=; \
	if [ ! -d $(LOCAL_SECRETS) ]; then ./scripts/local-secrets.sh >/dev/null; gen=1; fi; \
	trap 'rm -f "$$out"; [ -z "$$gen" ] || rm -rf $(LOCAL_SECRETS)' EXIT; \
	for o in $(addprefix services/,$(NAMESPACES)) overlays/eks overlays/local-postgres; do \
	  kubectl kustomize deploy/k8s/$$o >> "$$out"; echo --- >> "$$out"; done; \
	(cd deploy/rabbitmq/operators && shasum -a 256 -c --quiet SHA256SUMS) || { echo "✗ a vendored operator manifest changed (deploy/rabbitmq/operators/SHA256SUMS)"; exit 1; }; \
	for o in operators .; do kubectl kustomize deploy/rabbitmq/$$o >> "$$out"; echo --- >> "$$out"; done; \
	for d in deploy/k8s/overlays/vault-namespaces/*/; do [ ! -d "$$d" ] || { kubectl kustomize "$$d" >> "$$out"; echo --- >> "$$out"; }; done; \
	for f in infra/vault/tests/golden/*.yaml deploy/argocd/*.yaml deploy/argocd/apps/*.yaml; do [ ! -f "$$f" ] || { cat "$$f" >> "$$out"; echo --- >> "$$out"; }; done; \
	kubeconform -strict -summary -skip CustomResourceDefinition -schema-location default -schema-location '$(KUBECONFORM_CRDS)' - < "$$out"
	@$(MAKE) --no-print-directory k8s-check-images
	@./scripts/check_argocd.sh
	@./scripts/check_kustomize_refs.sh
	@bash scripts/tests/config_rolls_test.sh
	@cd backend && go run ./cmd/rabbitmq-topology -check

# kubectl gets its EKS token from `aws eks get-token`, so expired AWS credentials
# make every call fail with "the server has asked for the client to provide
# credentials" (2026-10-20). Say so before applying anything.
k8s-check-auth:
	@if ! kubectl auth whoami >/dev/null 2>&1; then \
	  echo "✗ kubectl is not logged in to $$(kubectl config current-context 2>/dev/null)."; \
	  aws sts get-caller-identity --query Arn --output text 2>&1 | sed '/^$$/d; s/^/  aws sts: /' | head -2; \
	  echo "  Refresh your AWS credentials, check with 'aws sts get-caller-identity', then run this again."; \
	  exit 1; fi
	@echo "✓ kubectl is logged in to $$(kubectl config current-context) as $$(aws sts get-caller-identity --query Arn --output text 2>/dev/null)"

# Every image the EKS overlay deploys must be a well-formed reference with a pinned
# tag or digest: no whitespace (a stray space in newTag left the migrate Job's pod in
# InvalidImageName until its deadline, 2026-10-19), no :latest, no missing tag.
IMAGE_REF := ^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)*(:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}|@sha256:[a-f0-9]{64})$$
# Every pinned ghcr.io image exists (network; not part of k8s-validate, which CI runs).
k8s-check-registry:
	./scripts/check_registry.sh

k8s-check-images:
	@imgs=$$(kubectl kustomize deploy/k8s/overlays/eks | sed -nE 's/^ *(- )?image: *//p' | tr -d "\"'" | sort -u); \
	bad=$$(printf '%s\n' "$$imgs" | grep -Ev '$(IMAGE_REF)'; printf '%s\n' "$$imgs" | grep -E ':latest$$'); \
	if [ -n "$$bad" ]; then echo "✗ malformed or unpinned images in overlays/eks (check newTag):"; printf '%s\n' "$$bad" | sed 's/^/  [/; s/$$/]/'; exit 1; fi; \
	echo "✓ $$(printf '%s\n' "$$imgs" | wc -l | tr -d ' ') images in overlays/eks, all pinned and well-formed"

# --- web/ (ryftapp.de, D107): one place for the site's checks; `make hooks` once per clone ---
# The drift checks and some tests read the app from ../app (RYFT_APP_DIR overrides it: absolute,
# or relative to web/).

web-install:
	cd web && npm ci && npx playwright install chromium

# Typecheck, ESLint + Prettier + brand + messages parity, Vitest. Add REQUIRE_SIBLINGS=1 to make
# a missing ../app an error instead of a skip.
web-test:
	cd web && npm run typecheck && npm run lint && npm test

# The static export in web/out (postbuild: CSP hashes, brand check of the output).
web-build:
	cd web && npm run build

# The nginx image (docker or podman), then Playwright against it: routes, headers, axe.
web-image:
	cd web && npm run image

web-serve: web-image
	cd web && npm run serve

web-serve-stop:
	cd web && npm run serve:stop

web-e2e: web-serve
	@cd web && for i in $$(seq 1 30); do curl -fs -o /dev/null http://localhost:8080/ && break; sleep 1; done; \
	npm run e2e; status=$$?; npm run serve:stop; exit $$status

# Regenerate src/styles/tokens.* from the app (../app/Ryft).
web-tokens:
	cd web && npm run tokens

# The brand guard on staged web/ files (pre-commit) and on every commit message (commit-msg).
hooks:
	git config core.hooksPath .githooks
	@echo "✓ git hooks: .githooks (brand check on staged web/ files and on commit messages)"

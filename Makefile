REGISTRY ?= ghcr.io/timkrebs/ryft
TAG      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
SERVICES := ingest scoring api migrate

# Docker if it is installed, otherwise Podman (its `compose` runs the same file).
CONTAINER ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || echo podman)
COMPOSE   ?= $(CONTAINER) compose

.PHONY: test build images push up down logs smoke smoke-supabase check-swift k8s-apply-eks k8s-apply-local k8s-delete-local k8s-validate

test:
	cd backend && go vet ./... && go test ./...

build:
	cd backend && for s in $(SERVICES); do CGO_ENABLED=0 go build -o bin/$$s ./cmd/$$s; done

images:
	for s in $(SERVICES); do \
	  $(CONTAINER) build -t $(REGISTRY)/$$s:$(TAG) -t $(REGISTRY)/$$s:latest --build-arg SERVICE=$$s backend; \
	done

push: images
	for s in $(SERVICES); do $(CONTAINER) push $(REGISTRY)/$$s:$(TAG); $(CONTAINER) push $(REGISTRY)/$$s:latest; done

# Local dev: Postgres + NATS + the three services via docker compose.
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
	AUTH_MODE=supabase ./scripts/smoke.sh

# After smoke, on macOS: decode every endpoint with the Swift mirror in docs/API.md
# and with the app's current models (../app). See scripts/check_swift.sh.
check-swift:
	./scripts/check_swift.sh

# One namespace per service (docs/ARCHITECTURE.md → Namespaces).
NAMESPACES := nats migrate ingest scoring api
LOCAL_SECRETS := deploy/k8s/overlays/local-postgres/secrets

# EKS (api.ryftapp.de), after infra/aws/kubernetes and infra/vault are applied
# (docs/OPERATIONS.md → EKS): the Vault Secrets Operator objects first, so each
# namespace has its Secret before the pods start; then the workloads. The finished
# migrate Job is deleted first because a Job's pod template cannot change.
k8s-apply-eks:
	for s in $(NAMESPACES); do kubectl apply -k deploy/k8s/overlays/vault-namespaces/$$s; done
	for s in $(NAMESPACES); do kubectl -n ryft-$$s wait --for=create secret/ryft-secrets --timeout=120s; done
	kubectl -n ryft-migrate delete job migrate --ignore-not-found
	kubectl apply -k deploy/k8s/overlays/eks
	kubectl -n ryft-migrate wait --for=condition=complete job/migrate --timeout=10m
	for s in ingest api scoring; do kubectl -n ryft-$$s rollout status deploy/$$s --timeout=10m; done

# Self-hosted (in-cluster Postgres, API key): generates the per-namespace secrets once.
k8s-apply-local:
	[ -d $(LOCAL_SECRETS) ] || ./scripts/local-secrets.sh
	kubectl -n ryft-migrate delete job migrate --ignore-not-found
	kubectl apply -k deploy/k8s/overlays/local-postgres

k8s-delete-local:
	kubectl delete -k deploy/k8s/overlays/local-postgres

# Render every overlay offline and validate it (no cluster needed): each service's
# namespace, EKS, local-postgres (with throwaway secrets if none exist), the
# Vault Secrets Operator manifests infra/vault generated (if any) and the golden
# copies its tests pin.
KUBECONFORM_CRDS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
k8s-validate:
	@set -e; out=$$(mktemp); gen=; \
	if [ ! -d $(LOCAL_SECRETS) ]; then ./scripts/local-secrets.sh >/dev/null; gen=1; fi; \
	trap 'rm -f "$$out"; [ -z "$$gen" ] || rm -rf $(LOCAL_SECRETS)' EXIT; \
	for o in $(addprefix services/,$(NAMESPACES)) overlays/eks overlays/local-postgres; do \
	  kubectl kustomize deploy/k8s/$$o >> "$$out"; echo --- >> "$$out"; done; \
	for d in deploy/k8s/overlays/vault-namespaces/*/; do [ ! -d "$$d" ] || { kubectl kustomize "$$d" >> "$$out"; echo --- >> "$$out"; }; done; \
	for f in infra/vault/tests/golden/*.yaml; do [ ! -f "$$f" ] || { cat "$$f" >> "$$out"; echo --- >> "$$out"; }; done; \
	kubeconform -strict -summary -schema-location default -schema-location '$(KUBECONFORM_CRDS)' - < "$$out"

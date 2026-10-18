REGISTRY ?= ghcr.io/timkrebs/ryft
TAG      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
SERVICES := ingest scoring api migrate

# Docker if it is installed, otherwise Podman (its `compose` runs the same file).
CONTAINER ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || echo podman)
COMPOSE   ?= $(CONTAINER) compose

.PHONY: test build images push up down logs smoke smoke-supabase check-swift k8s-apply k8s-apply-vault k8s-apply-eks k8s-delete k8s-validate

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

# Default overlay (deploy/k8s/secrets.yaml) or the Vault one; see docs/OPERATIONS.md.
k8s-apply:
	kubectl apply -k deploy/k8s

k8s-apply-vault:
	kubectl apply -k deploy/k8s/vault

# EKS (api.ryftapp.de): needs infra/aws/kubernetes applied and overlays/eks/secrets.yaml.
k8s-apply-eks:
	kubectl apply -k deploy/k8s/overlays/eks

k8s-delete:
	kubectl delete -k deploy/k8s

# Render base and the Vault, EKS and local-postgres overlays offline and validate
# them (no cluster needed). Overlays without a real secrets.yaml borrow
# secrets.example.yaml for the render and lose it again.
KUBECONFORM_CRDS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
OVERLAY_SECRETS  := deploy/k8s/overlays/eks/secrets.yaml deploy/k8s/overlays/local-postgres/secrets.yaml
k8s-validate:
	@set -e; out=$$(mktemp); borrowed=; \
	for f in $(OVERLAY_SECRETS); do if [ ! -f $$f ]; then cp deploy/k8s/secrets.example.yaml $$f; borrowed="$$borrowed $$f"; fi; done; \
	trap 'rm -f "$$out"; [ -z "$$borrowed" ] || rm -f $$borrowed' EXIT; \
	for o in base vault overlays/eks overlays/local-postgres; do kubectl kustomize deploy/k8s/$$o >> "$$out"; echo --- >> "$$out"; done; \
	kubeconform -strict -summary -schema-location default -schema-location '$(KUBECONFORM_CRDS)' - < "$$out"

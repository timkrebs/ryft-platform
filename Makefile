REGISTRY ?= ghcr.io/timkrebs/ryft
TAG      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
SERVICES := ingest scoring api

# Docker if it is installed, otherwise Podman (its `compose` runs the same file).
CONTAINER ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || echo podman)
COMPOSE   ?= $(CONTAINER) compose

.PHONY: test build images push up down logs smoke check-swift k8s-apply k8s-apply-vault k8s-apply-eks k8s-delete k8s-validate

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

# Render base, the Vault overlay and the EKS overlay offline and validate them (no
# cluster needed). The EKS overlay borrows secrets.example.yaml when no real
# secrets.yaml exists, and removes it again.
KUBECONFORM_CRDS := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json
EKS_SECRETS      := deploy/k8s/overlays/eks/secrets.yaml
k8s-validate:
	@set -e; out=$$(mktemp); borrowed=; \
	if [ ! -f $(EKS_SECRETS) ]; then cp deploy/k8s/secrets.example.yaml $(EKS_SECRETS); borrowed=1; fi; \
	trap 'rm -f "$$out"; [ -z "$$borrowed" ] || rm -f $(EKS_SECRETS)' EXIT; \
	for o in base vault overlays/eks; do kubectl kustomize deploy/k8s/$$o >> "$$out"; echo --- >> "$$out"; done; \
	kubeconform -strict -summary -schema-location default -schema-location '$(KUBECONFORM_CRDS)' - < "$$out"

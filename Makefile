REGISTRY ?= ghcr.io/timkrebs/ryft
TAG      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
SERVICES := ingest scoring api

.PHONY: test build images push up down logs smoke k8s-apply k8s-delete

test:
	cd backend && go vet ./... && go test ./...

build:
	cd backend && for s in $(SERVICES); do CGO_ENABLED=0 go build -o bin/$$s ./cmd/$$s; done

images:
	for s in $(SERVICES); do \
	  docker build -t $(REGISTRY)/$$s:$(TAG) -t $(REGISTRY)/$$s:latest --build-arg SERVICE=$$s backend; \
	done

push: images
	for s in $(SERVICES); do docker push $(REGISTRY)/$$s:$(TAG); docker push $(REGISTRY)/$$s:latest; done

# Local dev: Postgres + NATS + the three services via docker compose.
up:
	docker compose up --build -d

down:
	docker compose down -v

logs:
	docker compose logs -f ingest scoring api

# Push a synthetic night + workout through the stack and read the score back.
smoke:
	./scripts/smoke.sh

k8s-apply:
	kubectl apply -k deploy/k8s

k8s-delete:
	kubectl delete -k deploy/k8s

.PHONY: build test test-integration test-e2e lint vet cover ci dev-up dev-down dev-reset clean

build:
	go build ./...

test:
	go test ./... -race -count=1

test-integration:
	go test -tags=integration ./internal/testutil/... -race -count=1 -v

test-e2e:
	go test -tags=e2e ./e2e/... -race -count=1 -v

lint:
	golangci-lint run

vet:
	go vet ./...

cover:
	go test ./... -race -coverprofile=coverage.out -count=1
	go tool cover -func=coverage.out | tail -1

ci: vet lint build test

SERVICES := registry orchestrator operator mockoidc

build-images:
	@for svc in $(SERVICES); do \
		docker build -f deploy/images/Dockerfile.service --build-arg SERVICE=$$svc -t dark-factory/$$svc:dev . || exit 1; \
	done

KIND_CLUSTER ?= dark-factory

load-images: build-images
	@$(KIND) get clusters | grep -q $(KIND_CLUSTER) || (echo "no kind cluster $(KIND_CLUSTER)"; exit 1)
	@for svc in $(SERVICES); do $(KIND) load docker-image --name $(KIND_CLUSTER) dark-factory/$$svc:dev; done

KUBECTL ?= kubectl
KIND ?= kind

live-up: build-images
	$(KIND) get clusters | grep -q dark-factory || $(KIND) create cluster --config deploy/kind.yaml
	$(MAKE) load-images
	$(KUBECTL) apply -f deploy/kubernetes/crd
	$(KUBECTL) apply -f deploy/live/live.yaml
	@echo "waiting for rollouts..."
	$(KUBECTL) -n dark-factory rollout status deploy/registry deploy/orchestrator deploy/operator --timeout=180s || true
	@echo "dark-factory live: registry :30080, mockoidc :30081"

live-scenario:
	./scripts/live-scenario.sh

live-down:
	$(KIND) delete cluster --name dark-factory

dev-up:
	docker compose -f deploy/compose.dev.yaml up -d --wait
	@echo "Dev stack ready:"
	@echo "  Postgres  postgres://darkfactory:darkfactory-dev@localhost:5432/darkfactory"
	@echo "  Redis     localhost:6379"
	@echo "  DynamoDB  http://localhost:4566 (LocalStack)"
	@echo "  MockOIDC  http://localhost:8082  (token: curl 'http://localhost:8082/token?user=alice')"

dev-down:
	docker compose -f deploy/compose.dev.yaml down

dev-reset:
	docker compose -f deploy/compose.dev.yaml down -v
	docker compose -f deploy/compose.dev.yaml up -d --wait

clean:
	rm -f coverage.out

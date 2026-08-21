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

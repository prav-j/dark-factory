.PHONY: build test test-integration test-e2e lint vet cover ci clean

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

clean:
	rm -f coverage.out

.PHONY: build test lint vet cover ci clean

build:
	go build ./...

test:
	go test ./... -race -count=1

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

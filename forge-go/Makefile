.PHONY: build test test-e2e-ladder lint clean cross-compile fmt vet

GOCACHE ?= $(CURDIR)/.gocache
GOMODCACHE ?= $(CURDIR)/.gomodcache
GOPATH ?= $(CURDIR)/.gopath
GOLANGCI_LINT_CACHE ?= $(CURDIR)/.golangci-cache

export GOCACHE GOMODCACHE GOPATH GOLANGCI_LINT_CACHE

VERSION ?= $(shell git describe --tags --always --dirty)
COMMIT ?= $(shell git rev-parse --short HEAD)
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -ldflags "-X github.com/rustic-ai/forge/forge-go/version.Version=$(VERSION) -X github.com/rustic-ai/forge/forge-go/version.GitCommit=$(COMMIT) -X github.com/rustic-ai/forge/forge-go/version.BuildDate=$(DATE)"

build:
	go build $(LDFLAGS) -o bin/forge main.go

test:
	go test -v ./...

test-e2e-ladder:
	FORGE_E2E_ATELIER=1 go test -v ./e2e -run '^TestE2ELadder_' -count=1

lint:
	golangci-lint run

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -rf bin/ dist/

cross-compile:
	goreleaser build --snapshot --clean

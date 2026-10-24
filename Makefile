VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/overad9658/tagherd/internal/cli
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

.PHONY: build test lint fmt validate clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/tagherd ./cmd/tagherd

test:
	go test -race ./...

lint:
	go vet ./...
	golangci-lint run

fmt:
	gofmt -w ./cmd ./internal

# Validate the policy in ./policy with a fresh build.
validate: build
	./bin/tagherd policy validate --policy-dir ./policy

clean:
	rm -rf bin

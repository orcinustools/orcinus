# Orcinus Makefile.
# The dev toolchain is a user-local Go SDK; point GO at it if it is not on PATH.
GO ?= go
BIN ?= bin/orcinus
PKG := github.com/biznetgio/orcinus
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(PKG)/pkg/version.Version=$(VERSION) -X $(PKG)/pkg/version.GitCommit=$(COMMIT)

.PHONY: all build test e2e e2e-live tidy lint clean

all: build

build:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/orcinus

# Unit tests + the offline (conversion) e2e test.
test:
	$(GO) test ./...

# Offline end-to-end: builds the binary and asserts converted manifests.
e2e:
	$(GO) test ./test/e2e/ -run TestConvert -v

# Live single-node e2e: boots k3s in Docker and deploys to it.
# Requires Docker (see test/e2e/live_test.go for the guard env var).
e2e-live:
	ORCINUS_E2E_LIVE=1 $(GO) test ./test/e2e/ -run TestLive -v -timeout 15m

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin

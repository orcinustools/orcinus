# Orcinus Makefile.
# The dev toolchain is a user-local Go SDK; point GO at it if it is not on PATH.
GO ?= go
BIN ?= bin/orcinus
PKG := github.com/orcinustools/orcinus
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(PKG)/pkg/version.Version=$(VERSION) -X $(PKG)/pkg/version.GitCommit=$(COMMIT)

GORELEASER ?= goreleaser

.PHONY: all build test e2e e2e-live e2e-tls tidy lint clean dist snapshot release-check

all: build

build:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/orcinus

# Unit tests + the offline (conversion) e2e test.
test:
	$(GO) test ./...

# Offline end-to-end: builds the binary and asserts converted manifests.
e2e:
	$(GO) test ./test/e2e/ -run TestConvert -v

# Live e2e: boots a real cluster in Docker and drives orcinus against it.
# Requires Docker (see test/e2e/live_test.go for the guard env vars).
e2e-live:
	ORCINUS_E2E_LIVE=1 $(GO) test ./test/e2e/ -run TestLive -v -timeout 30m

# Live Ingress + Let's Encrypt e2e against a real public domain (LE staging).
# Requires ORCINUS_E2E_DOMAIN resolving to this host with inbound 80/443 open:
#   make e2e-tls ORCINUS_E2E_DOMAIN=example.com ORCINUS_E2E_DOCKER="sudo docker"
e2e-tls:
	ORCINUS_E2E_LIVE=1 $(GO) test ./test/e2e/ -run TestLiveIngressTLS -v -timeout 15m

tidy:
	$(GO) mod tidy

# Validate the release config.
release-check:
	$(GORELEASER) check

# Build multi-arch release artifacts locally (no publish) into ./dist.
snapshot:
	$(GORELEASER) release --snapshot --clean

# Alias.
dist: snapshot

clean:
	rm -rf bin dist

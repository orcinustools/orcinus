# Orcinus Makefile.
# The dev toolchain is a user-local Go SDK; point GO at it if it is not on PATH.
GO ?= go
BIN ?= bin/orcinus
PKG := github.com/orcinustools/orcinus
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X $(PKG)/pkg/version.Version=$(VERSION) -X $(PKG)/pkg/version.GitCommit=$(COMMIT)

GORELEASER ?= goreleaser

.PHONY: all build build-buildah test e2e e2e-live e2e-tls e2e-standalone orcinus-standalone runtime-asset tidy lint clean dist snapshot release-check

all: build

build:
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/orcinus

# Build tags for the buildah image-build backend. btrfs/devicemapper graph
# drivers are excluded (they need system headers); openpgp avoids gpgme/cgo.
BUILDAH_TAGS := orcinus_buildah containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper

# The Linux orcinus binary WITH the buildah image-build backend compiled in, so
# `orcinus image build` can run Dockerfile RUN / multi-stage steps daemonlessly.
# Requires CGO (gcc) and Linux — buildah's storage/reexec need cgo at runtime,
# so this cannot fold into the lean CGO_ENABLED=0 `build` target. Runtime needs
# an OCI runtime (crun/runc); `--isolation chroot` needs no extra backend.
build-buildah:
	CGO_ENABLED=1 $(GO) build -ldflags "$(LDFLAGS)" -tags "$(BUILDAH_TAGS)" -o bin/orcinus-buildah ./cmd/orcinus

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

# The standalone runtime asset (downloaded once into pkg/runtime/assets, gitignored).
# The standalone orcinus binary embeds it via go:embed + the `standalone` build tag.
RUNTIME_VERSION ?= v1.31.5+k3s1
runtime-asset:
	@test -f pkg/runtime/assets/k3s || ( mkdir -p pkg/runtime/assets && \
	  curl -fsSL -o pkg/runtime/assets/k3s \
	  https://github.com/k3s-io/k3s/releases/download/$(RUNTIME_VERSION)/k3s && \
	  chmod +x pkg/runtime/assets/k3s )

# The full orcinus binary WITH the standalone runtime built in. It can both drive
# a cluster (`orcinus cluster init --runtime standalone`) and BE the runtime
# (`orcinus runtime server ...`) — a single self-contained binary. Opt-in via
# -tags so the default `orcinus` binary stays lean.
orcinus-standalone: runtime-asset
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -tags standalone -o bin/orcinus-standalone ./cmd/orcinus

# Live e2e for the standalone runtime (runs `orcinus runtime server` in a clean
# container as a bare host, verifying the embed+exec path).
e2e-standalone: orcinus-standalone
	ORCINUS_E2E_LIVE=1 $(GO) test ./test/e2e/ -run TestLiveStandaloneRuntime -v -timeout 15m

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

# Building images — `orcinus build`

`orcinus build` produces **OCI / Docker-compatible images without a Docker
daemon**. A Docker image is nothing more than an OCI artifact — a JSON config,
a set of tar layers, and a manifest — so orcinus assembles that artifact
in-process. The result is loadable with `docker load`, pushable to any registry,
and runnable by any OCI runtime (containerd, CRI-O, Kubernetes, Docker).

It accepts the same inputs as `docker build` and `docker compose build`:

- **`docker build` style** — a build context + a `Dockerfile`.
- **`docker compose build` style** — the `build:` block of each service in
  `orcinus.yml` / a compose file.

## Two engines

| Engine | Needs a runtime? | Handles | Platform |
|--------|------------------|---------|----------|
| **native** (default) | No — nothing | `FROM`, `COPY`, `ADD`, `ENV`, `WORKDIR`, `CMD`, `ENTRYPOINT`, `EXPOSE`, `LABEL`, `USER`, `VOLUME`, `ARG`, `STOPSIGNAL`, `SHELL` | any (build linux/amd64 from macOS) |
| **buildah** | OCI runtime (crun/runc), no daemon | everything above **plus `RUN` and multi-stage** — full `docker build` | Linux only |

The native engine parses the Dockerfile and replays every non-executing
instruction as layer/config mutations using
[`go-containerregistry`](https://github.com/google/go-containerregistry). It
runs no code, so it cannot execute `RUN`. That case is routed to buildah.

`--engine auto` (default) uses native when the Dockerfile allows it and buildah
otherwise. Force one with `--engine native` or `--engine buildah`.

## Usage

```bash
# docker build style: one context → OCI layout dir + docker-load tar
orcinus build ./app -t myapp:v1 -o ./out/oci --tar myapp.tar

# docker compose build style: build every service with a build: section
orcinus build -f orcinus.yml --tar images.tar
orcinus build -f orcinus.yml web api            # only these services

# build args, labels, target stage, platform
orcinus build ./app -t myapp:v1 \
  --build-arg VERSION=1.2.3 --label team=payments \
  --target runtime --platform linux/arm64 -o ./out
```

### Outputs

At least one is required:

- `-o, --output <dir>` — an **OCI image layout** directory (`index.json` +
  `blobs/`), consumable by containerd/skopeo and other OCI tooling.
- `--tar <file>` — a single **image archive**. `--tar-format docker` (default)
  is `docker load -i <file>`-compatible; `--tar-format oci` writes an
  OCI-layout tar.
- `--push <ref>` — push to a registry, authenticating via the local Docker
  credential chain (`~/.docker/config.json`), exactly like `docker push`.

In compose mode with several services, `-o`/`--tar` are automatically qualified
per service (`out/<service>`, `images-<service>.tar`) so they don't collide.

## Compose `build:` support

`orcinus build` reads the standard compose build keys: `context`, `dockerfile`,
`dockerfile_inline`, `args`, `labels`, `target`, `platforms`, and `tags`. The
service `image:` is the primary tag; `build.tags` add more. Profiles are honored
with `--profile`.

```yaml
services:
  web:
    image: registry.example.com/web:v1   # primary tag
    build:
      context: ./web
      dockerfile: Dockerfile
      args:
        NODE_ENV: production
      tags:
        - registry.example.com/web:latest
```

## Enabling the buildah engine

Buildah is imported as a Go library and compiled in only with the
`orcinus_buildah` build tag on Linux, keeping the default cross-platform,
CGO-free binary lean (the same opt-in pattern as the embedded standalone
runtime). It **cannot** be folded into the default `orcinus` binary: buildah's
storage/reexec machinery needs `CGO_ENABLED=1` to work at runtime (a CGO-free
build compiles but fails with `parsing PID ""`), whereas the default binary is
deliberately `CGO_ENABLED=0` and static. So it ships as a separate Linux flavor,
`orcinus-buildah` (like `orcinus-standalone`).

Get it from a release archive (`orcinus-buildah_<ver>_linux_amd64.tar.gz`) or
build it on a Linux host:

```bash
make build-buildah        # → bin/orcinus-buildah (CGO=1, linux/amd64)
```

which is shorthand for:

```bash
CGO_ENABLED=1 go build \
  -tags "orcinus_buildah containers_image_openpgp exclude_graphdriver_btrfs exclude_graphdriver_devicemapper" \
  -o bin/orcinus-buildah ./cmd/orcinus
```

The binary is self-contained: it generates a docker-like containers
configuration at runtime (unqualified images resolve against `docker.io`, images
are accepted without signature verification), so **no `/etc/containers/*` setup
is required**.

### RUN isolation (`--isolation`)

`RUN` steps execute inside an isolated root filesystem. How that isolation is
created determines what the host needs:

| `--isolation` | Host requirements | Notes |
|---------------|-------------------|-------|
| `chroot` | **nothing extra** — uses the host network | Kaniko-like. Most dependency-light; recommended for build hosts/CI. Weaker isolation. |
| `oci` | an OCI runtime (`crun`/`runc`) + a network backend (`netavark`) | Full container isolation, like `docker build`. |
| `rootless` | `oci` requirements **plus** `/etc/subuid`+`/etc/subgid` entries, `newuidmap`/`newgidmap` (`uidmap`), and a rootless net helper (`pasta`/`slirp4netns`) | For unprivileged users. Also needs unprivileged user namespaces enabled. |
| _(auto, default)_ | buildah's choice based on privileges | |

Pick the OCI runtime with `--runtime crun|runc` when the default is unavailable
or incompatible.

```bash
# most portable: no OCI runtime / network backend needed
orcinus image build ./app -t myapp:v1 --isolation chroot -o ./out
```

Verified end-to-end on Linux (Ubuntu 24.04, kernel 6.8): a Dockerfile with
`RUN apk add curl` builds successfully via `--isolation chroot` with **no Docker
daemon, no OCI runtime, and no network backend** installed.

Without a buildah-enabled binary, a Dockerfile that uses `RUN` or multi-stage
fails fast with an explanation rather than producing a wrong image.

## Can orcinus build Docker-compatible images with no runtime at all?

Yes — for everything except `RUN`. The native engine needs **no daemon and no
container runtime of any kind**; it only reads and writes files, so it works on
macOS and produces linux images. Executing `RUN` fundamentally requires running
commands inside a root filesystem, which needs an executor: that is what the
buildah engine provides (daemonless, but backed by crun/runc).

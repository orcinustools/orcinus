# orcinus/base

A runtime base image for compiled programs (Go, Rust, C/C++), built from
AlmaLinux packages and stripped down to libraries and data:

| Kept | Removed |
|---|---|
| glibc (C.UTF-8 locale), libgcc, CA certificates, tzdata, `/etc/os-release`, the rpm database | shell, coreutils, every program in `/usr/bin` and `/usr/sbin`, dnf/rpm, OpenSSL, interpreters, docs, setuid bits |

- **Non-root by default:** `USER 65532:65532` (`nonroot`, home `/home/nonroot`), numeric so Kubernetes `runAsNonRoot` can check it.
- **Scannable:** the rpm database is kept, so trivy and grype see all 12 packages. At build time (AlmaLinux 9.8) trivy reported 0 vulnerabilities, against 6 MEDIUM for `almalinux:9-minimal`.
- **Small:** about 23 MB.

## Use

```dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /app .

FROM orcinus/base:9
COPY --from=build /app /app
ENTRYPOINT ["/app"]
```

A dynamically linked binary works as long as it needs only glibc and libgcc. A
program that needs OpenSSL or other libraries must bring them along (static
linking, or a `COPY` of the `.so` files), or use a fuller base.

Run it locked down. In compose for `orcinus deploy`:

```yaml
services:
  app:
    image: registry.example.com/app:1.0
    user: "65532:65532"
    read_only: true
    cap_drop: [ALL]
    tmpfs: [/tmp]
```

Sign it, and make the cluster run only signed images, with `orcinus sign`
([SIGNING.md](../../docs/SIGNING.md), example: [examples/signed-app](../../examples/signed-app)).

There is no shell to `orcinus exec` into. Debug with `kubectl debug` and an
ephemeral container instead.

## Build and test

```bash
make image-base-test                   # builds orcinus/base:9 and runs the smoke test
make image-base ALMA_VERSION=10        # AlmaLinux 10 (needs an x86-64-v3 CPU)
```

The smoke test (`test/`) runs a static Go binary and a dynamic glibc binary with
a read-only root filesystem, every capability dropped and no-new-privileges. It
checks the UID, TLS through the system CA bundle, time zones, a writable `/tmp`,
and that no shell exists.

Rebuild regularly (weekly, or when AlmaLinux ships a security update) so the
packages stay patched.

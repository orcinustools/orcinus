<p align="center">
  <img src="https://raw.githubusercontent.com/orcinustools/orcinus/main/.github/assets/orcinus-logo.svg" alt="Orcinus" width="260">
</p>

# Orcinus — Orchestrate the pod.

A lightweight cluster runtime that runs your **`docker-compose.yml` files and
Kubernetes manifests natively — no translation, no drama.** One binary: run a
cluster *and* deploy your compose files to it, no hand-written Kubernetes
manifests required.

- **Architecture & design:** [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md)
- **CLI usage guide:** [`docs/USAGE.md`](./docs/USAGE.md)
- **Deployment & update strategies:** [`docs/DEPLOYMENT.md`](./docs/DEPLOYMENT.md)
- **Cluster setup (single / multi-node / HA):** [`docs/CLUSTER.md`](./docs/CLUSTER.md)
- **Ingress & Traefik middlewares:** [`docs/INGRESS.md`](./docs/INGRESS.md)
- **Private image registries:** [`docs/REGISTRY.md`](./docs/REGISTRY.md)
- **HTTP REST API (+ OpenAPI/Swagger):** [`docs/API.md`](./docs/API.md)
- **Plugins & cluster add-ons:** [`docs/PLUGINS.md`](./docs/PLUGINS.md)
- **High-availability storage:** [`docs/HA-STORAGE.md`](./docs/HA-STORAGE.md)
- **Examples:** [`examples/`](./examples/README.md) (WordPress, Redis, monitoring, autoscale, rollout, ingress+TLS, …)

Orcinus embeds a lightweight Kubernetes runtime and **forks kompose**
(`third_party/kompose`) so the compose→Kubernetes conversion is fully Docker
Compose compatible and under our control.

## Install

```bash
# Latest release binary (Linux/macOS, amd64/arm64)
curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install.sh | sh

# Standalone: self-contained binary with the runtime built in (linux/amd64)
curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install-standalone.sh | sh

# Or with Go
go install github.com/orcinustools/orcinus/cmd/orcinus@latest
```

Prebuilt archives + checksums are attached to each [GitHub release]
(https://github.com/orcinustools/orcinus/releases), produced by goreleaser.

## Build

The dev toolchain uses a user-local Go SDK.

```bash
make build            # → bin/orcinus
make test             # unit tests + offline conversion e2e
make e2e-live         # boots a real single-node cluster and deploys to it
make snapshot         # build multi-arch release artifacts into ./dist (goreleaser)
make release-check    # validate the release config
```

## Usage

```bash
# Start a cluster (writes ~/.orcinus/kubeconfig; needs a container runtime)
bin/orcinus cluster init
bin/orcinus cluster join                     # add a local node (reads saved state)
bin/orcinus cluster status                   # cluster + node status
bin/orcinus cluster down                     # stop + remove the cluster

# Deploy — with no -f, orcinus.yml (or a compose file) is auto-detected
bin/orcinus deploy --wait
bin/orcinus deploy -f examples/orcinus.yml
bin/orcinus deploy -f https://example.com/app.yaml   # from a URL, like kubectl

# Convert only (no cluster needed)
bin/orcinus deploy -f examples/docker-compose.yml --dry-run
bin/orcinus deploy -f examples/docker-compose.yml --dry-run -o out/

# Deploy to a cluster (server-side apply + prune)
bin/orcinus deploy -f examples/docker-compose.yml --kubeconfig ~/.kube/config --wait

# Inspect and tear down
bin/orcinus ls                       # list managed projects
bin/orcinus ps myapp                 # a project's pods + status
bin/orcinus logs web -f              # stream a service's logs
bin/orcinus rm myapp                 # remove a project
```

## `x-orcinus-*` extensions

Add Kubernetes hints directly in your compose file (see `docs/ARCHITECTURE.md` §7):

```yaml
services:
  web:
    image: nginx:1.27
    ports: ["80:80"]
    x-orcinus-expose: ingress        # ingress | nodeport | loadbalancer | clusterip
    x-orcinus-host: web.local
  db:
    image: postgres:16
    x-orcinus-controller: statefulset # deployment | statefulset | daemonset
    x-orcinus-volume-size: 5Gi
    x-orcinus-secret: [POSTGRES_PASSWORD]
```

## License

MIT. See [`LICENSE`](./LICENSE) and [`NOTICE`](./NOTICE) (third-party attribution).

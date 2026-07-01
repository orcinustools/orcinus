# Orcinus

A lightweight Kubernetes distribution (based on **k3s**) that **natively
understands `docker-compose.yml`**. One binary: run a cluster *and* deploy your
compose files to it, no hand-written k8s manifests required.

- **Architecture & design:** [`ARCHITECTURE.md`](./ARCHITECTURE.md)
- **CLI specification:** [`CLI.md`](./CLI.md)

Orcinus **imports k3s & kubectl as libraries** and **forks kompose**
(`third_party/kompose`) so the compose→k8s conversion is fully Docker Compose
compatible and under our control.

## Status

| Milestone | What | State |
|---|---|---|
| M0 | Scaffold, multicall CLI, `--help` | ✅ done |
| M1 | `orcinus deploy` compose→k8s conversion (+ `x-orcinus-*`) | ✅ done |
| M2 | Apply/prune to a cluster (client-go) | ⏳ next |
| M3 | Embedded k3s runtime (`init`/`join`/`kubectl`) | ⏳ later |

## Build

The dev toolchain uses a user-local Go SDK.

```bash
make build            # → bin/orcinus
make test             # unit tests + offline conversion e2e
make e2e              # offline end-to-end (no cluster needed)
make e2e-live         # boots k3s in Docker and deploys to it (needs Docker)
```

## Try it (works today, no cluster)

```bash
# Convert a compose file to Kubernetes manifests
bin/orcinus deploy -f examples/docker-compose.yml --dry-run

# Write the manifests to a directory
bin/orcinus deploy -f examples/docker-compose.yml --dry-run -o out/
```

## `x-orcinus-*` extensions

Add Kubernetes hints directly in your compose file (see `ARCHITECTURE.md` §8):

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

Orcinus translates these onto the forked kompose engine's native per-service
labels, then decorates every object with ownership labels
(`app.kubernetes.io/managed-by=orcinus`).

## License

Apache-2.0. See [`LICENSE`](./LICENSE) and [`NOTICE`](./NOTICE).

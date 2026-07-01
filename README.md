# Orcinus

A lightweight Kubernetes distribution that **natively understands
`docker-compose.yml`**. One binary: run a cluster *and* deploy your compose files
to it, no hand-written Kubernetes manifests required.

- **Architecture & design:** [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md)
- **CLI specification:** [`docs/CLI.md`](./docs/CLI.md)

Orcinus embeds a lightweight Kubernetes runtime and **forks kompose**
(`third_party/kompose`) so the compose→Kubernetes conversion is fully Docker
Compose compatible and under our control.

## Status

| Milestone | What | State |
|---|---|---|
| M0 | Scaffold, multicall CLI, `--help` | ✅ done |
| M1 | `orcinus deploy` compose→k8s conversion (+ `x-orcinus-*`) | ✅ done |
| M2 | Cluster ops: `deploy` (apply/prune/wait), `rm`, `ls`, `ps`, `logs` | ✅ done |
| M3 | Cluster runtime: `init` / `join` (writes kubeconfig automatically) | ✅ done |

Verified compose→k8s mappings: controllers (Deployment/StatefulSet/DaemonSet),
Service (ClusterIP/NodePort), Ingress, PVC, Secret extraction, replicas,
resource limits/requests, healthcheck→liveness probe, multiple ports.

## Build

The dev toolchain uses a user-local Go SDK.

```bash
make build            # → bin/orcinus
make test             # unit tests + offline conversion e2e
make e2e-live         # boots a real single-node cluster and deploys to it
```

## Usage

```bash
# Start a cluster (writes ~/.orcinus/kubeconfig; needs a container runtime)
bin/orcinus init
bin/orcinus join                     # add a local node (reads saved state)

# Deploy — with no -f, orcinus.yml (or a compose file) is auto-detected
bin/orcinus deploy --wait
bin/orcinus deploy -f examples/orcinus.yml

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

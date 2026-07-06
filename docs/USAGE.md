# Orcinus — Usage Guide

Complete command-line reference for **orcinus**: a lightweight Kubernetes
distribution that natively understands `docker-compose.yml`. One binary runs a
cluster *and* deploys your compose files to it.

For design and internals, see [`ARCHITECTURE.md`](./ARCHITECTURE.md).

---

## Table of Contents

- [1. Overview](#1-overview)
- [2. Installation](#2-installation)
- [3. Core Concepts](#3-core-concepts)
  - [3.1 Multicall binary](#31-multicall-binary)
  - [3.2 Projects & ownership labels](#32-projects--ownership-labels)
  - [3.3 Kubeconfig resolution](#33-kubeconfig-resolution)
  - [3.4 Input auto-detection](#34-input-auto-detection)
  - [3.5 Default file discovery](#35-default-file-discovery)
- [4. Quick Start](#4-quick-start)
- [5. Command Reference](#5-command-reference)
  - [5.1 `cluster init`](#51-orcinus-cluster-init)
  - [5.2 `cluster join`](#52-orcinus-cluster-join)
  - [5.3 `cluster status`](#53-orcinus-cluster-status)
  - [5.4 `cluster down`](#54-orcinus-cluster-down)
  - [5.5 `deploy`](#55-orcinus-deploy)
  - [5.6 `rm`](#56-orcinus-rm)
  - [5.7 `ls`](#57-orcinus-ls)
  - [5.8 `ps`](#58-orcinus-ps)
  - [5.9 `logs`](#59-orcinus-logs)
  - [5.10 `scale`](#510-orcinus-scale)
  - [5.11 `autoscale`](#511-orcinus-autoscale)
  - [5.12 `rollback`](#512-orcinus-rollback)
  - [5.13 `secret`](#513-orcinus-secret)
  - [5.14 `plugin`](#514-orcinus-plugin)
  - [5.15 `kubectl`](#515-orcinus-kubectl)
  - [5.16 `version`](#516-orcinus-version)
  - [5.17 `completion`](#517-orcinus-completion)
  - [5.18 `node`](#518-orcinus-node)
- [6. Datastore](#6-datastore)
- [7. Volumes & storage](#7-volumes--storage)
- [8. Placement & node constraints](#8-placement--node-constraints)
- [Appendix A — Compose → Kubernetes mapping](#appendix-a--compose--kubernetes-mapping)
- [Appendix B — `x-orcinus-*` extension reference](#appendix-b--x-orcinus--extension-reference)
- [Appendix C — Environment variables & files](#appendix-c--environment-variables--files)
- [Appendix D — Exit codes & behavior](#appendix-d--exit-codes--behavior)
- [Appendix E — Troubleshooting](#appendix-e--troubleshooting)

---

## 1. Overview

Orcinus follows a **Docker Swarm-like** UX: few commands, familiar verbs.

| You want to… | Command |
|---|---|
| Start a cluster | `orcinus cluster init` |
| Add a node | `orcinus cluster join` |
| Inspect the cluster | `orcinus cluster status` |
| Tear the cluster down | `orcinus cluster down` |
| Deploy an app | `orcinus deploy` |
| Remove an app | `orcinus rm <project>` |
| List apps | `orcinus ls` |
| List an app's pods | `orcinus ps <project>` |
| Tail logs | `orcinus logs <service>` |
| Scale a service | `orcinus scale <service> <replicas>` |
| Autoscale a service | `orcinus autoscale <service> --max N` |
| Roll back a bad release | `orcinus rollback <service>` |
| Manage secrets / TLS certs | `orcinus secret create[-tls] …` |
| Add a cluster add-on | `orcinus plugin install <name>` |
| Inspect / label nodes | `orcinus node ls` · `orcinus node label` |
| Drop to kubectl | `orcinus kubectl <args>` |
| Serve the REST API | `orcinus api` (see [`API.md`](./API.md)) |

Commands fall into two groups: **cluster lifecycle** (`init`, `join`, `status`,
`down`) and **workloads** (`deploy`, `rm`, `ls`, `ps`, `logs`, `scale`,
`autoscale`, `rollback`, `secret`, `plugin`, `kubectl`).

---

## 2. Installation

```bash
# Latest release binary (Linux/macOS, amd64/arm64)
curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install.sh | sh

# Or with Go
go install github.com/orcinustools/orcinus/cmd/orcinus@latest
```

The cluster-lifecycle commands (`init`/`join`/`status`/`down`) require a container
runtime on the host. Workload commands only need access to a cluster.

**Standalone binary.** Each release also attaches `orcinus-standalone`
(linux/amd64) — a single self-contained binary with the runtime built in, for the
`--runtime standalone` provider (no container runtime needed):

```bash
curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install-standalone.sh | sh
# equivalently: ORCINUS_STANDALONE=1 curl -fsSL …/install.sh | sh
```

See [CLUSTER.md → Runtime providers](CLUSTER.md#runtime-providers).

**Releases & versioning.** Releases are **tag-driven**: pushing a `vX.Y.Z` tag
triggers GitHub Actions → GoReleaser, which builds all binaries, stamps the
version (`orcinus version` reports it), and publishes a GitHub Release with
archives + `checksums.txt`.

---

## 3. Core Concepts

### 3.1 Multicall binary

`orcinus` is a single binary; the first argument selects the subcommand
(`orcinus <command> [flags]`). Run `orcinus <command> --help` for per-command help.

### 3.2 Projects & ownership labels

Every resource orcinus creates is stamped with ownership labels:

- `app.kubernetes.io/managed-by=orcinus`
- `app.kubernetes.io/part-of=<project>` and `orcinus.io/project=<project>`

A **project** groups the resources of one deployment. It defaults to the current
directory name and can be set with `--project`. Projects power `ls`, `ps`, `rm`,
and prune. These labels are also stamped on pod templates so `ps`/`logs` can find
the right pods.

### 3.3 Kubeconfig resolution

Workload commands resolve the target cluster in this order:

1. `--kubeconfig <path>` flag
2. `$KUBECONFIG`
3. `~/.orcinus/kubeconfig` (written by `orcinus cluster init`)
4. `~/.kube/config`

So after `orcinus cluster init`, the other commands work with no extra configuration.

### 3.4 Input auto-detection

`orcinus deploy` classifies **each YAML document** independently:

- has `apiVersion` **and** `kind` → **Kubernetes manifest** (applied as-is)
- has a top-level `services:` → **compose** (converted, then applied)
- neither → a clear error

Force the mode with `--as compose` or `--as manifest`. Multi-document files may
mix both kinds.

### 3.5 Default file discovery

With no `-f`, `deploy` searches the current directory in this order and uses the
first match:

```
orcinus.yml → orcinus.yaml → compose.yaml → compose.yml → docker-compose.yml → docker-compose.yaml
```

So an `orcinus.yml` in your project is picked up automatically.

---

## 4. Quick Start

```bash
# 1. Start a single-node cluster (writes ~/.orcinus/kubeconfig)
orcinus cluster init

# 2. Deploy — no -f needed if orcinus.yml / a compose file is present
orcinus deploy --wait

# 3. Inspect
orcinus ls
orcinus ps myapp
orcinus logs web -f

# 4. Tear down the app, then the cluster
orcinus rm myapp
orcinus cluster down
```

---

## 5. Command Reference

### 5.1 `orcinus cluster init`

Start a single-node cluster on this machine. Writes `~/.orcinus/kubeconfig` and
records cluster state. **Idempotent**: re-running against a running cluster reuses
it; a stopped cluster of the same name is refused (run `orcinus cluster down` first).

```
orcinus cluster init [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--name <str>` | `orcinus` | Cluster / server name |
| `--image <str>` | built-in | Cluster runtime image |
| `--port <n>` | `6443` | Host port for the API server |
| `--http-port <n>` | off | Publish ingress HTTP on this host port (e.g. `80`) — needed to serve web traffic |
| `--https-port <n>` | off | Publish ingress HTTPS on this host port (e.g. `443`) |
| `--bind <ip>` | `127.0.0.1` | Host interface to publish the API port on (`0.0.0.0` = all) |
| `--advertise <host>` | — | Address other nodes/clients use to reach this server (adds a TLS SAN; enables remote join) |
| `--token <str>` | auto | Join token for other nodes |
| `--cluster-init` | `false` | Embedded etcd (HA mode) — see [§6](#6-datastore) |
| `--datastore-endpoint <str>` | SQLite | External datastore — see [§6](#6-datastore) |
| `--kubeconfig <path>` | `~/.orcinus/kubeconfig` | Where to write the kubeconfig |
| `--runtime <docker\|standalone>` | `docker` | Runtime provider — see the note below |
| `--server-arg <arg>` | — | Extra runtime server argument (repeatable), e.g. `--server-arg --snapshotter=native` |

```bash
orcinus cluster init                              # safe default: API bound to 127.0.0.1
orcinus cluster init --name prod --port 6550
orcinus cluster init --advertise 10.0.0.5         # reachable by remote nodes/clients
```

On success it prints the kubeconfig path and a ready-to-paste `orcinus cluster join …`.

> **Runtime provider (`--runtime`).** The default `docker` provider runs the
> cluster in a container — no privileges beyond a container runtime, works
> anywhere. The `standalone` provider runs the runtime **natively on the host** as
> a managed process with **no container runtime required** — a single
> self-contained binary. It is opt-in: only the binary built with
> `make orcinus-standalone` has the runtime built in (the default binary returns a
> clear error). It needs root and a real host (systemd-style cgroups). See
> [CLUSTER.md → Runtime providers](CLUSTER.md#runtime-providers).

> **Networking & security.** By default the API server is published on
> `127.0.0.1` only — reachable from this machine, not the network. This is the
> safe default and is single-host (server + local agents). To let nodes on **other
> hosts** join, set `--advertise <ip-or-host>` (which also opens the bind to all
> interfaces and adds the address to the TLS cert). Only do this behind a firewall
> you trust; the join token grants cluster access.

### 5.2 `orcinus cluster join`

Join a node to an existing cluster. With no flags, reads the saved cluster state
(so on the init host, `orcinus cluster join` just works). Use `--role server` to
add another **master** (control plane), or `--role agent` (default) to add a
**worker**. See [`CLUSTER.md`](./CLUSTER.md) for full topologies.

```
orcinus cluster join [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--role <agent\|server>` | `agent` | `agent` = worker, `server` = control-plane/master |
| `--server <url>` | from saved state | Cluster server URL |
| `--token <str>` | from saved state | Join token |
| `--name <str>` | `<cluster>-<role>` | Node/container name |
| `--image <str>` | from saved state | Cluster runtime image |

```bash
orcinus cluster join                                        # worker, on the init host
orcinus cluster join --role agent  --server https://10.0.0.5:6443 --token <token>
orcinus cluster join --role server --server https://10.0.0.5:6443 --token <token>
```

> Adding masters (`--role server`) requires the cluster to have an HA datastore
> (`init --cluster-init` or `--datastore-endpoint`); the default SQLite datastore
> supports a single master only.

### 5.3 `orcinus cluster status`

Show cluster status: name, running state, kubeconfig path, and node list.

```
orcinus cluster status [--name <str>]
```

```bash
orcinus cluster status
```

### 5.4 `orcinus cluster down`

Stop and remove the cluster — the server **and all joined nodes** — then clear the
local kubeconfig and state.

```
orcinus cluster down [--name <str>]
```

```bash
orcinus cluster down
```

### 5.5 `orcinus deploy`

⭐ The main command. Auto-detects compose vs manifest and applies to the cluster.

```
orcinus deploy [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-f, --file <path\|url>` | auto-detect | Input file, `http(s)://` URL, or `-` (stdin); repeatable (see [§3.5](#35-default-file-discovery)) |
| `-n, --namespace <ns>` | `default` | Target namespace |
| `--project <name>` | directory name | Ownership label (for `ls`/`ps`/`rm`/prune) |
| `--as <compose\|manifest>` | auto | Force the input mode |
| `--dry-run` | `false` | Render instead of applying |
| `-o, --output <dir>` | — | Also write converted manifests to a directory |
| `--prune` | `true` | Remove owned resources no longer in the input |
| `--wait` | `false` | Wait until workloads are ready |
| `--acme-email <email>` | — | Auto-install cert-manager when `x-orcinus-tls` is used |
| `--replicas <n>` | `1` | Default replicas when a service specifies none |
| `--pvc-size <size>` | `1Gi` | Default PersistentVolumeClaim size |
| `--kubeconfig <path>` | auto | Target cluster (see [§3.3](#33-kubeconfig-resolution)) |

```bash
orcinus deploy                                   # auto-detect a default file
orcinus deploy -f docker-compose.yml --wait
orcinus deploy -f https://example.com/app.yaml   # fetch over HTTP(S), like kubectl
orcinus deploy -f app.yml -f ingress.yaml        # mix compose + manifest
cat manifest.yaml | orcinus deploy -f -          # from stdin
orcinus deploy -f docker-compose.yml --dry-run   # print manifests, don't apply
orcinus deploy -f docker-compose.yml --dry-run -o out/   # write manifests to a dir
```

> Convert-only workflow: `deploy --dry-run [-o dir]` replaces a separate
> `convert` command.

#### Deployment strategies

Each service becomes a Deployment with a rolling update by default. You can:

- tune it via the standard compose `deploy.update_config`,
- switch to `recreate` via `x-orcinus-strategy`, or
- do **canary / blue-green** via `x-orcinus-rollout` (Argo Rollouts,
  auto-installed).

```yaml
services:
  web:
    image: myapp:2
    ports: ["80"]
    x-orcinus-rollout: canary            # or: bluegreen
```

**Full guide → [`DEPLOYMENT.md`](./DEPLOYMENT.md)** (update_config mapping,
recreate, canary/blue-green, and what isn't mapped). The relevant `x-orcinus-*`
keys are in [Appendix B](#appendix-b--x-orcinus--extension-reference). Examples:
[`examples/deploy-strategies`](../examples/deploy-strategies/orcinus.yml),
[`examples/rollout`](../examples/rollout/orcinus.yml).

### 5.6 `orcinus rm`

Remove all resources of a project.

```
orcinus rm <project> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-n, --namespace <ns>` | `default` | Namespace to remove from |
| `--kubeconfig <path>` | auto | Target cluster |

```bash
orcinus rm myapp
```

### 5.7 `orcinus ls`

List orcinus-managed projects with workload counts and namespaces.

```
orcinus ls [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-A, --all-namespaces` | `true` | List across all namespaces |
| `-n, --namespace <ns>` | all | Restrict to a namespace |
| `--kubeconfig <path>` | auto | Target cluster |

```bash
orcinus ls
orcinus ls -n staging
```

Output columns: `PROJECT  WORKLOADS  NAMESPACES`.

### 5.8 `orcinus ps`

List the pods of a project.

```
orcinus ps <project> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-n, --namespace <ns>` | all | Restrict to a namespace |
| `--kubeconfig <path>` | auto | Target cluster |

```bash
orcinus ps myapp
```

Output columns: `SERVICE  POD  READY  STATUS  RESTARTS  NODE`.

### 5.9 `orcinus logs`

Stream logs of a service. If several pods back the service, streams run
concurrently, each line prefixed with the pod name.

```
orcinus logs <service> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-f, --follow` | `false` | Follow the log stream |
| `-n, --namespace <ns>` | `default` | Namespace |
| `--project <name>` | — | Further scope to a project |
| `--kubeconfig <path>` | auto | Target cluster |

```bash
orcinus logs web
orcinus logs web -f --project myapp
```

### 5.10 `orcinus scale`

Set the replica count of a service's Deployment or StatefulSet.

```
orcinus scale <service> <replicas> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-n, --namespace <ns>` | `default` | Namespace |
| `--kubeconfig <path>` | auto | Target cluster |

```bash
orcinus scale web 3            # scale the web service to 3 replicas
orcinus scale db 1 -n staging  # in the "staging" namespace
```

Output: `scaled Deployment/web to 3 replicas`.

### 5.11 `orcinus autoscale`

Create or update a **HorizontalPodAutoscaler** (HPA) for a service. Orcinus
clusters ship with a working metrics-server (auto-enabled by `cluster init`), so
HPAs get metrics out of the box. The workload's containers need **resource
requests** for percentage-based targets to work.

```
orcinus autoscale <service> --max <n> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--min <n>` | `1` | Minimum replicas |
| `--max <n>` | — (**required**) | Maximum replicas |
| `--cpu <pct>` | `80` if no metric set | Target average CPU utilization % |
| `--memory <pct>` | — | Target average memory utilization % |
| `-n, --namespace <ns>` | `default` | Namespace |
| `--kubeconfig <path>` | auto | Target cluster |

```bash
orcinus autoscale web --min 2 --max 8 --cpu 70        # scale on 70% CPU
orcinus autoscale worker --max 5 --memory 75          # scale on 75% memory
```

The HPA targets the service's Deployment or StatefulSet. It is created if absent
and updated in place if it already exists. Inspect it with `kubectl get hpa`.
Example: [`examples/autoscale`](../examples/autoscale/orcinus.yml).

You can also declare autoscaling directly in the compose file, so it is created
on `deploy` (see [Appendix B](#appendix-b--x-orcinus--extension-reference)):

```yaml
services:
  web:
    image: myapp
    deploy:
      resources:
        reservations: { cpus: "0.1", memory: 64M }   # requests are needed for % HPA
    x-orcinus-autoscale-min: 2
    x-orcinus-autoscale-max: 8
    x-orcinus-autoscale-cpu: 70
```

### 5.12 `orcinus rollback`

Roll a service back to its previous revision. Works for a Deployment, StatefulSet,
or Argo Rollout.

```
orcinus rollback <service> [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-n, --namespace <ns>` | `default` | Namespace |
| `--kubeconfig <path>` | auto | Target cluster |

```bash
orcinus rollback web            # revert web to the prior revision
```

### 5.13 `orcinus secret`

Manage Kubernetes Secrets — including bring-your-own TLS certs.

```
orcinus secret create <name> --from-literal KEY=VALUE [...]
orcinus secret create-tls <name> --cert <file> --key <file>
orcinus secret create-registry <name> --server <host> -u <user> -p <pass>
orcinus secret ls
orcinus secret rm <name>
```

| Subcommand | Purpose |
|---|---|
| `create` | Opaque secret from `--from-literal KEY=VALUE` (repeatable) |
| `create-tls` | TLS secret from a PEM cert + key — reference with `x-orcinus-tls-secret` |
| `create-registry` | Private-registry pull secret — **tests the login first**, then reference with `x-orcinus-image-pull-secret` (`--insecure`, `--skip-login-check`; see [`REGISTRY.md`](./REGISTRY.md)) |
| `ls` | List secrets (name, type, key count, whether orcinus-managed) |
| `rm` | Delete a secret |

All take `-n`/`--namespace` (default `default`) and `--kubeconfig`. Created
secrets are labelled `managed-by=orcinus`.

```bash
orcinus secret create db-creds --from-literal PASSWORD=s3cr3t
orcinus secret create-tls mysite-cert --cert fullchain.pem --key privkey.pem
orcinus secret create-registry regcred --server ghcr.io -u me -p "$GHCR_PAT"
```

### 5.14 `orcinus plugin`

Manage cluster add-ons (see [`PLUGINS.md`](./PLUGINS.md)).

```
orcinus plugin list
orcinus plugin info <name>
orcinus plugin install <name> [--email ...] [--staging]
orcinus plugin install --profile web|observability
orcinus plugin remove <name>
```

```bash
orcinus plugin list
orcinus plugin info cert-manager
orcinus plugin install cert-manager --email me@example.com
orcinus plugin install ingress-nginx
orcinus plugin remove metrics-server
```

Catalog: `cert-manager`, `ingress-nginx`, `metrics-server`, `monitoring`,
`argo-rollouts`, `dashboard`, `registry`, `grafana`, `storage` (providers:
`local-path`/`longhorn`/`nfs`/`minio`/`rook-ceph`). Versions are pinned. Install a
set with `--profile web|observability`. See [`PLUGINS.md`](./PLUGINS.md).

```bash
orcinus plugin install storage --provider minio --size 20Gi
orcinus plugin install storage --provider minio --replicas 4   # distributed/HA
orcinus plugin install storage --provider nfs --nfs-server 10.0.0.9 --nfs-path /export
```

For fault-tolerant storage across nodes, see [`HA-STORAGE.md`](./HA-STORAGE.md).

### 5.15 `orcinus kubectl`

Escape hatch: run `kubectl` against the orcinus cluster. Uses your local `kubectl`
(with the resolved kubeconfig) if installed, otherwise the cluster container's
bundled kubectl. All arguments pass straight through.

```bash
orcinus kubectl get pods -A
orcinus kubectl describe deploy web
orcinus kubectl logs deploy/web
```

### 5.16 `orcinus version`

Print the orcinus version, git commit, and the embedded conversion-engine ref.

```bash
orcinus version
```

### 5.17 `orcinus completion`

Generate a shell completion script (bash, zsh, fish, powershell).

```bash
orcinus completion bash > /etc/bash_completion.d/orcinus
source <(orcinus completion zsh)
```

### 5.18 `orcinus node`

Inspect and label cluster nodes — node labels back placement constraints
([§8](#8-placement--node-constraints)), like `docker node update --label-add`.

```bash
orcinus node ls                                  # NAME, STATUS, ROLES, VERSION
orcinus node label <node> zone=east disktype=ssd # add/update labels
orcinus node label <node> --rm disktype          # remove a label
```

---

## 6. Datastore

`orcinus cluster init` stores cluster state in a datastore. The default is **SQLite**;
other backends are opt-in.

| Mode | HA? | How |
|---|---|---|
| **SQLite** (default) | ❌ | `orcinus cluster init` |
| **etcd embedded** | ✅ (odd node count) | `orcinus cluster init --cluster-init` |
| **etcd external** | ✅ | `--datastore-endpoint https://etcd:2379` |
| **PostgreSQL** | ✅ | `--datastore-endpoint "postgres://…"` |
| **MySQL / MariaDB** | ✅ | `--datastore-endpoint "mysql://…"` |

Endpoint forms:

| Backend | Form |
|---|---|
| external etcd | `https://host:2379` (comma-separated for multiple) |
| PostgreSQL | `postgres://user:pass@host:port/db[?sslmode=…]` |
| MySQL/MariaDB | `mysql://user:pass@tcp(host:port)/db` |

**Notes.** SQLite is not HA — for multiple control-plane nodes use embedded etcd
or an external datastore. Keep DB credentials in an env/secret, not in shell
history.

---

## 7. Volumes & storage

Compose `volumes:` map to Kubernetes storage **by type** — orcinus picks the right
kind automatically:

| Compose entry | Becomes | Use it for |
|---|---|---|
| named volume — `data:/var/lib/data` | **PersistentVolumeClaim** | persistent app data managed by the cluster |
| bind mount — `./conf:/etc/app`, `/srv/x:/data` | **hostPath** (node-local) | mounting a folder from the node, like a Compose/Swarm bind mount |

### Named volumes (PVC)

```yaml
services:
  db:
    image: postgres:16
    volumes:
      - pgdata:/var/lib/postgresql/data
    x-orcinus-volume-size: 5Gi        # PVC size (else --pvc-size, default 1Gi)
volumes:
  pgdata:                             # declare named volumes, like compose
```

- Bound by the cluster's **default StorageClass** — `local-path` works out of the
  box. For replicated / networked / object storage, install the storage plugin
  (see [`PLUGINS.md`](./PLUGINS.md) and [`HA-STORAGE.md`](./HA-STORAGE.md)).
- Size per service with `x-orcinus-volume-size`, or globally with `--pvc-size`.

### Bind mounts (hostPath)

```yaml
services:
  app:
    image: nginx:1.27
    volumes:
      - /srv/app/config:/etc/app:ro   # absolute host path, read-only
      - ./data:/data                  # relative → resolved to an absolute path
```

- Mounts a **host folder** into the container — **node-local**: the path lives on
  whichever node the pod runs on, exactly like a Compose/Swarm bind mount.
- Relative paths (`./data`) resolve to absolute; a `:ro` suffix → read-only; the
  folder is auto-created if missing.
- **Single-node / `--runtime standalone`:** it's simply your local folder.
  **Multi-node:** the path must exist on the scheduled node — for data shared
  across nodes use a named volume (PVC) on a networked StorageClass instead.

---

## 8. Placement & node constraints

Pin services to specific nodes with Docker Swarm's `deploy.placement` — orcinus
maps it to Kubernetes scheduling automatically:

| Swarm | Kubernetes |
|---|---|
| `placement.constraints: [key == value]` | `nodeAffinity` (required) `In` |
| `placement.constraints: [key != value]` | `nodeAffinity` (required) `NotIn` |
| `placement.preferences: [{spread: key}]` | `topologySpreadConstraints` (soft) |

Supported constraint keys:

| Swarm key | Maps to node label |
|---|---|
| `node.role == manager` / `worker` | presence of `node-role.kubernetes.io/control-plane` (Exists / DoesNotExist) |
| `node.hostname` | `kubernetes.io/hostname` |
| `node.platform.arch` | `kubernetes.io/arch` (e.g. `amd64`, `arm64`) |
| `node.platform.os` | `kubernetes.io/os` |
| `node.labels.<x>` | `<x>` (a custom node label) |

An unknown key is a **hard error** (so typos don't silently no-op).

**Workflow** — label the nodes, then constrain the service:

```bash
orcinus node label worker-1 zone=east disktype=ssd
```

```yaml
services:
  api:
    image: myapi:1.0
    deploy:
      placement:
        constraints:
          - node.role == worker         # keep off control-plane nodes
          - node.labels.zone == east    # only zone=east nodes
          - node.platform.arch == amd64
        preferences:
          - spread: node.labels.zone    # spread replicas across zones
```

For a plain node pin without Swarm syntax, use the extension:

```yaml
    x-orcinus-node-selector:
      disktype: ssd
```

**Notes.**
- Constraints are **required** (hard): if no node matches, the pod stays
  **Pending** (unschedulable) — check with `orcinus ps <project>`.
- Preferences (`spread`) are **soft** — best-effort, never block scheduling.
- On a single-node cluster the one node is a control-plane node, so
  `node.role == worker` will leave pods Pending; use `== manager` (or omit it).

---

## Appendix A — Compose → Kubernetes mapping

| Compose element | Kubernetes object | Notes |
|---|---|---|
| `service` | `Deployment` (default) | override via `x-orcinus-controller` |
| `ports` | `Service` (ClusterIP) | multiple ports supported; `x-orcinus-expose` changes type |
| named `volumes` | `PersistentVolumeClaim` | size via `x-orcinus-volume-size` |
| bind mount (`./x:/y`, `/abs:/y`) | `hostPath` volume | node-local, like a Compose/Swarm bind mount (relative paths resolve to absolute); host folder mounted into the container |
| `environment` / `env_file` | container `env` | secrets via `x-orcinus-secret` |
| `deploy.mode` | `Deployment` (replicated) / `DaemonSet` (global) | `global` → one pod per node (like Swarm); override with `x-orcinus-controller` |
| `deploy.replicas` | `.spec.replicas` | |
| `deploy.resources` | `resources.limits/requests` | cpu + memory |
| `deploy.update_config` | `.spec.strategy` (+ minReadySeconds/progressDeadline) | order/parallelism/delay/monitor |
| `deploy.placement` | `nodeAffinity` + `topologySpreadConstraints` | constraints/preferences — see [§8](#8-placement--node-constraints) |
| `healthcheck` | `livenessProbe` | exec/http from the compose healthcheck |
| `restart` | `restartPolicy` / controller-managed | |
| `depends_on` | best-effort apply ordering | no complex readiness guarantee |
| `networks` | (ignored) | flat Kubernetes networking |

Volumes are covered in detail in [§7](#7-volumes--storage) (named → PVC, bind
mount → node-local hostPath).

**Known limitations.** These compose features have no direct Kubernetes
equivalent and are intentionally **not** mapped: `networks` (Kubernetes uses flat
cluster networking — reach a service by its name), `depends_on` conditions /
`links` (apply ordering is best-effort, no startup gating), `build:` (bring a
pre-built image; orcinus does not build), and Swarm-only `deploy` keys beyond
`replicas`/`resources`/`update_config`. Add anything unmapped as a raw Kubernetes
manifest in the same file (it's applied as-is).

---

## Appendix B — `x-orcinus-*` extension reference

Add Kubernetes hints directly inside a compose service. Compose ignores `x-*`
keys; orcinus parses them during conversion.

| Key | Values | Effect |
|---|---|---|
| `x-orcinus-controller` | `deployment` \| `statefulset` \| `daemonset` | Controller kind (default: deployment) |
| `x-orcinus-expose` | `clusterip` \| `nodeport` \| `loadbalancer` \| `ingress` | How the service is exposed |
| `x-orcinus-host` | hostname, or comma-separated list | Ingress host(s) (with `x-orcinus-expose: ingress`). Multiple domains → one rule each + a TLS cert covering all (see [`INGRESS.md`](./INGRESS.md#1a-multiple-domains-for-one-service)) |
| `x-orcinus-volume-size` | e.g. `5Gi` | PVC request size for the service's volumes |
| `x-orcinus-secret` | list of env var names | Move those env vars into a `Secret` (referenced via `secretKeyRef`) |
| `x-orcinus-tls` | ClusterIssuer name (e.g. `letsencrypt`) | Adds a TLS block + `cert-manager.io/cluster-issuer` annotation (needs the `cert-manager` plugin) |
| `x-orcinus-tls-secret` | existing TLS Secret name | Serve a **custom/BYO cert** from that Secret (no cert-manager); wins over `x-orcinus-tls` |
| `x-orcinus-path` | path (default `/`) | Ingress path |
| `x-orcinus-port` | port number | Which service port the ingress routes to |
| `x-orcinus-ingress-class` | `traefik` \| `nginx` \| … | Ingress class to use |
| `x-orcinus-strip-prefix` | `true` \| prefix \| list | Traefik **StripPrefix**: strip the path prefix so the backend sees `/` (see [INGRESS.md](./INGRESS.md)) |
| `x-orcinus-middleware` | middleware name or list | Attach Traefik **middleware(s)** to the route, in order (rate limit, headers, auth, redirect, …) |
| `x-orcinus-image-pull-secret` | secret name or list | Attach **imagePullSecret(s)** for a private registry (see [`REGISTRY.md`](./REGISTRY.md)) |
| `x-orcinus-node-selector` | map of `label: value` | Pin the pod to nodes with these labels (k8s `nodeSelector`); see [§8](#8-placement--node-constraints) |
| `x-orcinus-autoscale-min` | int | HPA min replicas (default 1) |
| `x-orcinus-autoscale-max` | int | HPA max replicas (**enables** the HPA) |
| `x-orcinus-autoscale-cpu` | int | HPA target CPU utilization % (default 80 if no metric) |
| `x-orcinus-autoscale-memory` | int | HPA target memory utilization % |
| `x-orcinus-strategy` | `rolling` \| `recreate` | Deployment update strategy (default rolling) |
| `x-orcinus-max-surge` | int or % (e.g. `1`, `25%`) | Rolling: extra pods created during an update |
| `x-orcinus-max-unavailable` | int or % (e.g. `0`, `25%`) | Rolling: pods that may be down during an update |
| `x-orcinus-rollout` | `canary` \| `bluegreen` | Emit an Argo **Rollout** instead of a Deployment (progressive delivery) |

Example:

```yaml
services:
  web:
    image: nginx:1.27
    ports: ["80:80"]
    x-orcinus-expose: ingress
    x-orcinus-host: web.local
  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=s3cr3t
    volumes:
      - dbdata:/var/lib/postgresql/data
    x-orcinus-controller: statefulset
    x-orcinus-volume-size: 5Gi
    x-orcinus-secret: [POSTGRES_PASSWORD]
volumes:
  dbdata: {}
```

HTTPS example (needs `orcinus plugin install cert-manager` and the cluster's
ingress ports published — see [`PLUGINS.md`](./PLUGINS.md)):

```yaml
services:
  web:
    image: myapp
    ports: ["8080"]
    x-orcinus-expose: ingress
    x-orcinus-host: app.example.com
    x-orcinus-tls: letsencrypt        # trusted cert via cert-manager
```

### Reuse with YAML anchors (Docker-Compose-style extensions)

`orcinus.yml` is parsed with the official Compose parser, so the whole
[Docker Compose extension](https://docs.docker.com/reference/compose-file/extension/)
experience works: define a top-level `x-` block once, anchor it with `&`, and
merge it into services with `<<: *anchor`. Per-service keys override the merged
defaults.

```yaml
# reusable defaults (top-level x- fields are ignored as services)
x-web-defaults: &web
  restart: always
  x-orcinus-expose: ingress
  x-orcinus-host: app.local

services:
  frontend:
    <<: *web                # inherits expose + host
    image: nginx:1.27
    ports: ["80"]
  api:
    <<: *web
    image: myapi:1.0
    ports: ["8080"]
    x-orcinus-host: api.local   # override the merged default
```

This produces two Ingresses (`app.local`, `api.local`) with no duplication.

---

## Appendix C — Environment variables & files

**Environment variables**

| Variable | Used by | Purpose |
|---|---|---|
| `KUBECONFIG` | workload commands | Target cluster (see [§3.3](#33-kubeconfig-resolution)) |
| `ORCINUS_DOCKER` | `init`/`join`/`status`/`down` | Container command (default `docker`; e.g. `sudo docker`) |

**Files**

| Path | Written by | Contents |
|---|---|---|
| `~/.orcinus/kubeconfig` | `init` | Kubeconfig for the local cluster |
| `~/.orcinus/cluster.json` | `init` | Cluster state (name, server URL, token) used by `join`/`status`/`down` |

---

## Appendix D — Exit codes & behavior

- Exit `0` on success; non-zero on error, with a message on stderr prefixed
  `error:`.
- `deploy` uses **server-side apply** (field manager `orcinus`) — it is
  idempotent and safe to re-run.
- `--prune` only removes resources within the current `--project` scope; it never
  prunes without a project.
- `deploy` writes progress notes (e.g. `using orcinus.yml`, `applied N object(s)`)
  to **stderr**; rendered manifests (`--dry-run`) go to **stdout**, so
  `deploy --dry-run > out.yaml` is clean.

---

## Appendix E — Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `no kubeconfig found` | Run `orcinus cluster init`, pass `--kubeconfig`, or set `$KUBECONFIG`. |
| `a cluster named "…" already exists but is not running` | Run `orcinus cluster down` first, then `init`. |
| `no input file found` | No `-f` and no default file present — pass `-f` or add `orcinus.yml`. |
| `document is neither a compose file … nor a k8s manifest` | The YAML lacks both `services:` and `apiVersion`+`kind`; fix it or force with `--as`. |
| `init`/`join` fail to reach the runtime | Ensure a container runtime is available; set `ORCINUS_DOCKER` (e.g. `sudo docker`). |
| Manifests need a namespace that doesn't exist | Create the namespace, or deploy to `default`. |

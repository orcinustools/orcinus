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
  - [5.10 `version`](#510-orcinus-version)
  - [5.11 `completion`](#511-orcinus-completion)
- [6. Datastore](#6-datastore)
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

Commands fall into two groups: **cluster lifecycle** (`init`, `join`, `status`,
`down`) and **workloads** (`deploy`, `rm`, `ls`, `ps`, `logs`).

---

## 2. Installation

```bash
# Latest release binary (Linux/macOS, amd64/arm64)
curl -fsSL https://raw.githubusercontent.com/biznetgio/orcinus/main/install.sh | sh

# Or with Go
go install github.com/biznetgio/orcinus/cmd/orcinus@latest
```

The cluster-lifecycle commands (`init`/`join`/`status`/`down`) require a container
runtime on the host. Workload commands only need access to a cluster.

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
| `--token <str>` | auto | Join token for other nodes |
| `--cluster-init` | `false` | Embedded etcd (HA mode) — see [§6](#6-datastore) |
| `--datastore-endpoint <str>` | SQLite | External datastore — see [§6](#6-datastore) |
| `--kubeconfig <path>` | `~/.orcinus/kubeconfig` | Where to write the kubeconfig |

```bash
orcinus cluster init
orcinus cluster init --name prod --port 6550
```

On success it prints the kubeconfig path and a ready-to-paste `orcinus cluster join …`.

### 5.2 `orcinus cluster join`

Join a node to an existing cluster. With no flags, reads the saved cluster state
(so on the init host, `orcinus cluster join` just works).

```
orcinus cluster join [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--server <url>` | from saved state | Cluster server URL |
| `--token <str>` | from saved state | Join token |
| `--name <str>` | `<cluster>-agent` | Agent node/container name |
| `--image <str>` | from saved state | Cluster runtime image |

```bash
orcinus cluster join                                        # on the init host
orcinus cluster join --server https://10.0.0.5:6443 --token <token>
```

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
| `-f, --file <path>` | auto-detect | Input file; repeatable; `-` = stdin (see [§3.5](#35-default-file-discovery)) |
| `-n, --namespace <ns>` | `default` | Target namespace |
| `--project <name>` | directory name | Ownership label (for `ls`/`ps`/`rm`/prune) |
| `--as <compose\|manifest>` | auto | Force the input mode |
| `--dry-run` | `false` | Render instead of applying |
| `-o, --output <dir>` | — | Also write converted manifests to a directory |
| `--prune` | `true` | Remove owned resources no longer in the input |
| `--wait` | `false` | Wait until workloads are ready |
| `--replicas <n>` | `1` | Default replicas when a service specifies none |
| `--pvc-size <size>` | `1Gi` | Default PersistentVolumeClaim size |
| `--kubeconfig <path>` | auto | Target cluster (see [§3.3](#33-kubeconfig-resolution)) |

```bash
orcinus deploy                                   # auto-detect a default file
orcinus deploy -f docker-compose.yml --wait
orcinus deploy -f app.yml -f ingress.yaml        # mix compose + manifest
cat manifest.yaml | orcinus deploy -f -          # from stdin
orcinus deploy -f docker-compose.yml --dry-run   # print manifests, don't apply
orcinus deploy -f docker-compose.yml --dry-run -o out/   # write manifests to a dir
```

> Convert-only workflow: `deploy --dry-run [-o dir]` replaces a separate
> `convert` command.

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

### 5.10 `orcinus version`

Print the orcinus version, git commit, and the embedded conversion-engine ref.

```bash
orcinus version
```

### 5.11 `orcinus completion`

Generate a shell completion script (bash, zsh, fish, powershell).

```bash
orcinus completion bash > /etc/bash_completion.d/orcinus
source <(orcinus completion zsh)
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

## Appendix A — Compose → Kubernetes mapping

| Compose element | Kubernetes object | Notes |
|---|---|---|
| `service` | `Deployment` (default) | override via `x-orcinus-controller` |
| `ports` | `Service` (ClusterIP) | multiple ports supported; `x-orcinus-expose` changes type |
| named `volumes` | `PersistentVolumeClaim` | size via `x-orcinus-volume-size` |
| `environment` / `env_file` | container `env` | secrets via `x-orcinus-secret` |
| `deploy.replicas` | `.spec.replicas` | |
| `deploy.resources` | `resources.limits/requests` | cpu + memory |
| `healthcheck` | `livenessProbe` | exec/http from the compose healthcheck |
| `restart` | `restartPolicy` / controller-managed | |
| `depends_on` | best-effort apply ordering | no complex readiness guarantee |
| `networks` | (ignored in v1) | flat Kubernetes networking |

---

## Appendix B — `x-orcinus-*` extension reference

Add Kubernetes hints directly inside a compose service. Compose ignores `x-*`
keys; orcinus parses them during conversion.

| Key | Values | Effect |
|---|---|---|
| `x-orcinus-controller` | `deployment` \| `statefulset` \| `daemonset` | Controller kind (default: deployment) |
| `x-orcinus-expose` | `clusterip` \| `nodeport` \| `loadbalancer` \| `ingress` | How the service is exposed |
| `x-orcinus-host` | hostname | Ingress host (with `x-orcinus-expose: ingress`) |
| `x-orcinus-volume-size` | e.g. `5Gi` | PVC request size for the service's volumes |
| `x-orcinus-secret` | list of env var names | Move those env vars into a `Secret` (referenced via `secretKeyRef`) |

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

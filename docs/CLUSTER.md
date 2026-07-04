# Orcinus — Cluster Setup Guide

How to create orcinus clusters: **single-node**, **single-master + workers**, and
**HA multi-master + workers**.

For the full command/flag reference see [`USAGE.md`](./USAGE.md).

---

## Table of Contents

- [Concepts](#concepts)
- [Prerequisites](#prerequisites)
- [Runtime providers](#runtime-providers)
- [Topology 1 — Single node](#topology-1--single-node)
- [Topology 2 — One master + workers](#topology-2--one-master--workers)
- [Topology 3 — HA: multiple masters + workers](#topology-3--ha-multiple-masters--workers)
- [Verifying the cluster](#verifying-the-cluster)
- [Tearing down](#tearing-down)
- [All on one host (for testing)](#all-on-one-host-for-testing)
- [Notes & gotchas](#notes--gotchas)

---

## Concepts

- **Node roles.** A **server** node runs the control plane (a "master"); an
  **agent** node runs only workloads (a "worker").
  - `orcinus cluster init` creates the **first server**.
  - `orcinus cluster join --role server` adds **another master**.
  - `orcinus cluster join --role agent` (default) adds a **worker**.
- **Token.** Every node joins with the cluster's join token. `orcinus cluster
  init` prints a ready-to-paste `join` command containing it.
- **kubeconfig.** `init` writes `~/.orcinus/kubeconfig` on the machine you ran it
  on; `deploy`/`ls`/`ps`/`rm` use it automatically there.
- **Datastore = HA capability.** The default datastore (SQLite) supports **only a
  single master**. To run **multiple masters** you must create the cluster with an
  HA datastore — either embedded etcd (`--cluster-init`) or an external datastore
  (`--datastore-endpoint`, see [`USAGE.md` §6](./USAGE.md#6-datastore)).
- **Reachability.** By default the API is published on `127.0.0.1` (safe, local
  only). For nodes on **other hosts** to join, the first master must be started
  with `--advertise <ip-or-host>` so the address is reachable and in the TLS cert.

---

## Prerequisites

- A container runtime on each host (for the default `docker` provider). The
  docker command is taken from `$ORCINUS_DOCKER` (default `docker`; e.g. `sudo
  docker`). The `standalone` provider needs **no** container runtime — see below.
- Network reachability between hosts on the API port (default `6443`) for
  multi-node setups, and a firewall you trust.

---

## Runtime providers

`orcinus cluster init` supports two runtime providers via `--runtime`:

| Provider | How it runs | Needs | Best for |
|---|---|---|---|
| `docker` (default) | Cluster runs inside a container | A container runtime | Dev, CI, homelab, most setups |
| `standalone` | Runtime runs **natively on the host** as a managed process | Root + a real host (systemd-style cgroups) | Bare-metal / edge with **no container runtime** |

### `docker` (default)

Nothing special — this is the container-backed path used throughout this guide.

```bash
orcinus cluster init
```

### `standalone` — single self-contained binary

The runtime is compiled **into** the orcinus binary (`go:embed`), so one binary
both drives the cluster and *is* the runtime. There is no container runtime and
no image pull.

It is **opt-in at build time**: only the binary built with `make orcinus-standalone`
has the runtime built in. The normal `orcinus` binary returns a clear error if you
pass `--runtime standalone`, keeping the default binary small.

**Install** the prebuilt `orcinus-standalone` (linux/amd64, attached to every
GitHub release):

```bash
curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install-standalone.sh | sh
# → installs `orcinus-standalone` to /usr/local/bin
# overrides: ORCINUS_VERSION=<tag>  ORCINUS_INSTALL=<dir>
```

Or build it yourself:

```bash
# Build the standalone binary (downloads the runtime asset once, then embeds it).
make orcinus-standalone
```

Then start a native cluster (the examples below assume it's on `PATH` as
`orcinus-standalone`; use `sudo ./bin/orcinus-standalone` if you built locally):

```bash
# Start a native cluster — no container runtime involved. Needs root.
sudo orcinus-standalone cluster init --runtime standalone --port 6443

# Pass extra runtime server flags with --server-arg (repeatable):
sudo orcinus-standalone cluster init --runtime standalone \
  --server-arg --disable=traefik --server-arg --disable=servicelb

# The same binary is also the runtime's kubectl:
sudo orcinus-standalone kubectl get nodes

# Tear down (stops the process, unmounts, clears state):
sudo orcinus-standalone cluster down
```

**Multi-node (native).** Install `orcinus-standalone` on each **additional host**
and join it natively — no container runtime anywhere:

```bash
# worker (agent)
sudo orcinus-standalone cluster join --runtime standalone \
  --server https://<master-ip>:6443 --token <token>

# extra control-plane (needs an HA-datastore cluster: init with --cluster-init)
sudo orcinus-standalone cluster join --runtime standalone --role server \
  --server https://<master-ip>:6443 --token <token>
```

One standalone node per host (`orcinus cluster down` stops the local node). Run
each `join` on its **own** host — two native nodes can't share one host.

**Notes & limits.**
- Needs **root** — the native runtime manages cgroups, iptables and mounts.
- Needs a **real host** with cgroup delegation (systemd-style). Running it
  *nested inside another container* hits a cgroup-v2 delegation limit; use the
  `docker` provider there instead.
- State lives under `~/.orcinus/runtime/<name>/` (of the user that ran it — i.e.
  `/root/.orcinus` under `sudo`), separate from any `docker`-provider cluster.
- `orcinus cluster down` reaps the server plus its containerd shims, unmounts
  everything it created, and removes the CNI interfaces (`cni0`, `flannel.1`) and
  kube/flannel iptables rules — leaving the host as it found it (other rules, e.g.
  Docker's, are preserved).

---

## Topology 1 — Single node

Everything (control plane + workloads) on one machine. Simplest; great for dev,
edge, CI, homelab.

```bash
orcinus cluster init
orcinus deploy -f docker-compose.yml --wait
```

That's it — no join needed. The API stays on `127.0.0.1`.

---

## Topology 2 — One master + workers

One control-plane node, N workers. Suitable when you want capacity/redundancy for
workloads but don't need control-plane HA.

**On the master host** — advertise a reachable address:

```bash
orcinus cluster init --advertise 10.0.0.10
```

Note the printed join command (it contains the token), e.g.:

```
orcinus cluster join --server https://10.0.0.10:6443 --token <token>
```

**On each worker host:**

```bash
orcinus cluster join --role agent \
  --server https://10.0.0.10:6443 --token <token>
```

Deploy from the master (or anywhere with its kubeconfig):

```bash
orcinus deploy -f docker-compose.yml --wait
```

---

## Topology 3 — HA: multiple masters + workers

Multiple control-plane nodes so the cluster survives losing a master, plus
workers. Requires an HA datastore. Use an **odd number of masters** (3 or 5) so
etcd keeps quorum.

**On the first master** — embedded etcd + advertise:

```bash
orcinus cluster init --cluster-init --advertise 10.0.0.10
```

Grab the token from its output (or reuse the printed join command).

**On the 2nd and 3rd master hosts** — join as `server`:

```bash
orcinus cluster join --role server \
  --server https://10.0.0.10:6443 --token <token>
```

**On each worker host** — join as `agent`:

```bash
orcinus cluster join --role agent \
  --server https://10.0.0.10:6443 --token <token>
```

Result: a 3-master control plane (etcd quorum) with as many workers as you add.

> Alternative to embedded etcd: an **external datastore** for HA — replace
> `--cluster-init` with `--datastore-endpoint "postgres://…"` (or etcd/MySQL) on
> every master. See [`USAGE.md` §6](./USAGE.md#6-datastore).

---

## Verifying the cluster

```bash
orcinus cluster status          # cluster name, state, kubeconfig, node list
orcinus ls                      # deployed projects
orcinus ps <project>            # a project's pods and which node they're on
```

`status` shows every node and its role; masters carry the `control-plane` role.

---

## Tearing down

```bash
orcinus cluster down            # stops & removes ALL nodes of the cluster + local state
```

`down` removes the server and every joined node (tracked by an
`orcinus.cluster=<name>` label) and clears `~/.orcinus`.

---

## All on one host (for testing)

You can exercise every topology on a single host — this is exactly what the e2e
tests do. Give each node a unique `--name` and the first master a mapped `--port`:

```bash
# 3-node HA on one machine
orcinus cluster init --name demo --port 16445 --cluster-init
orcinus cluster join --role server --name demo-2
orcinus cluster join --role agent  --name demo-w1
orcinus cluster down --name demo
```

On the init host, `join` reads the saved cluster state, so `--server`/`--token`
can be omitted.

---

## Notes & gotchas

- **Single master needs no special datastore.** Multiple masters **do** —
  `--cluster-init` or `--datastore-endpoint`. Trying to add a `server` node to a
  default (SQLite) cluster will not form an HA control plane.
- **Odd master count.** etcd needs a majority; 3 masters tolerate 1 failure, 5
  tolerate 2. Two masters give no fault tolerance.
- **`--advertise` is required for cross-host joins.** Without it the API is
  loopback-only and only same-host agents can join. `--advertise` also opens the
  bind to all interfaces and adds the address to the TLS certificate.
- **Security.** The join token grants cluster membership — treat it as a secret,
  and only expose the API to networks you trust.
- **Workers don't need the datastore.** Only masters care about the datastore;
  `join --role agent` just needs `--server` + `--token`.

# Orcinus — Architecture & Plan

> A lightweight Kubernetes distribution that *natively* understands
> `docker-compose.yml`. One self-contained binary runs a cluster **and** deploys
> your compose files to it — no hand-written Kubernetes manifests required.
>
> **Status:** M0–M3 implemented and tested (cluster runtime via a runtime-manager
> provider); a fully in-binary embed is a future option.
> **Implementation language:** Go.
> **Strategy:** a Go project that embeds a lightweight Kubernetes runtime as a
> library, and **forks kompose** so compose conversion is fully Docker Compose
> compatible and entirely under orcinus's control.

---

## 1. Vision & Scope

Orcinus is a single binary that combines two capabilities:

1. **Cluster runtime** — runs a lightweight Kubernetes control plane + agent
   (suitable for edge, dev, CI, homelab).
2. **Compose-native** — a user only needs a `docker-compose.yml`, then
   `orcinus deploy` turns it into Kubernetes objects and deploys them to the
   cluster without writing Kubernetes YAML by hand.

Target user experience:

```bash
# Start a single-node cluster (control plane)
orcinus cluster init

# Deploy straight from compose or a manifest (auto-detected)
orcinus deploy -f docker-compose.yml                    # convert + apply in one shot
orcinus deploy -f docker-compose.yml --dry-run -o out   # convert to manifests only
orcinus rm myapp                                         # remove a project's resources
```

**In scope (v1):**
- Single binary, multicall (cluster runtime + the compose/deploy subcommands).
- Compose → Deployment/Service/PVC/ConfigMap/Secret/Ingress conversion.
- Idempotent `deploy` / `rm` via ownership labels.

**Out of scope (v1):**
- Compose orchestration features with no Kubernetes equivalent (e.g. complex
  `depends_on` with layered healthchecks) — approached best-effort.
- Multi-cluster / HA control plane (later).

---

## 2. Design Principles

1. **Upstream as a library where possible; fork only when full control is needed.**
   The cluster runtime is embedded as a library so orcinus can ride upstream
   releases. **kompose is *forked*** because we need full control over the
   conversion engine to truly follow the Docker Compose specification (see §3) —
   the fork is actively maintained (periodic rebase), not a dead/rebranded fork.
2. **One binary, many faces (multicall).** The `argv[0]` name / first subcommand
   selects the mode.
3. **Compose is a first-class citizen.** Conversion is not a separate step; it is
   integrated into the deploy flow.
4. **Idempotent & rollback-able.** Every generated resource gets ownership labels
   (`app.kubernetes.io/managed-by=orcinus` + project) so `deploy`/`rm` are safe to
   repeat, and `deploy --prune` removes resources that left the input.
5. **Zero-config, but overridable.** Sensible defaults; everything configurable
   via flags / compose annotations (`x-orcinus-*`).

---

## 3. The kompose Fork

Although kompose can be imported as a library, orcinus **forks** it
(`third_party/kompose`, wired via a `replace` directive). Reasons:

- **Full Compose compatibility.** The fork's loader is anchored to
  `compose-spec/compose-go` (the official Docker Compose parser); with a fork we
  can close feature gaps directly instead of waiting on upstream.
- **Full control over the transformer.** The `x-orcinus-*` extensions (§7),
  ownership labels, and mapping rules can be woven into the conversion pipeline.
- **Iteration speed.** No waiting on upstream merges for mapping fixes.

**Fork obligations:** actively maintained — periodic rebase to upstream, retain
Apache-2.0 license headers & attribution (see `NOTICE`).

**Current wiring:** orcinus translates `x-orcinus-*` keys onto the fork's native
per-service labels, hands the compose files to the fork's loader → transformer,
then decorates the resulting objects with ownership labels.

---

## 4. High-Level Architecture

```
                         ┌──────────────────────────────────────┐
                         │              orcinus (1 binary)        │
  argv dispatch ───────► │  cmd/orcinus  (multicall router)       │
                         │   ├─ cluster  → pkg/cluster (init/join)│
                         │   │             (status/down)          │
                         │   ├─ deploy   ┐                        │
                         │   │           ├→ pkg/compose (fork)    │
                         │   └─ rm       ┘   + pkg/deploy (apply) │
                         └──────────────────────────────────────┘
                                     │                    │
                        embed/import │                    │ client-go
                                     ▼                    ▼
                    ┌────────────────────────┐   ┌───────────────────┐
                    │ embedded Kubernetes    │   │  Kubernetes API    │
                    │ runtime (cp + agent)   │◄──┤  (orcinus cluster) │
                    └────────────────────────┘   └───────────────────┘
```

Flow of `orcinus deploy -f docker-compose.yml` (document detected as compose):

```
compose file ─► pkg/compose.Convert()   (forked loader → transformer → []Object)
             ─► decorate                 (managed-by labels, namespace, x-orcinus-*)
             ─► pkg/deploy.Apply()       (server-side apply via client-go, + prune)
```

---

## 5. Components

| Package | Responsibility | Status |
|---|---|---|
| `cmd/orcinus` | Entry point, multicall router, cobra wiring | ✅ |
| `pkg/compose` | Load compose, transform to k8s objects, decorate, x-orcinus-* | ✅ |
| `pkg/detect`  | Classify each YAML doc as compose vs manifest | ✅ |
| `pkg/deploy`  | Render, server-side apply, prune, wait | ✅ |
| `pkg/cluster` | Provision the cluster runtime (init/join), write kubeconfig | ✅ |
| `pkg/version` | Build version, embedded component versions | ✅ |

---

## 6. Compose → Kubernetes Mapping (summary)

| Compose element | Kubernetes object | Notes |
|---|---|---|
| `service` | `Deployment` (default) | override via `x-orcinus-controller: statefulset/daemonset` |
| `ports` | `Service` (ClusterIP) | publish via `x-orcinus-expose: ingress/nodeport` |
| `volumes` (named) | `PersistentVolumeClaim` | size via `x-orcinus-volume-size` |
| `environment` / `env_file` | `env` + `ConfigMap`/`Secret` | secrets marked with `x-orcinus-secret` |
| `deploy.replicas` | `.spec.replicas` | |
| `deploy.resources` | `resources.limits/requests` | cpu/memory mapped and unit-tested |
| `healthcheck` | `livenessProbe` | exec/http probe derived from the compose healthcheck |
| `x-orcinus-autoscale-*` | `HorizontalPodAutoscaler` | min/max/cpu/memory → HPA for the service |
| `restart` | `restartPolicy` / managed by controller | |
| `depends_on` | best-effort apply ordering | no complex readiness guarantee |
| `networks` | (ignored in v1) | flat Kubernetes networking |

---

## 7. The `x-orcinus-*` Extensions

Orcinus uses `x-*` extension keys inside a service for Kubernetes hints. Compose
ignores `x-*` keys, so orcinus parses them itself and applies them during
conversion.

```yaml
services:
  web:
    image: nginx:1.27
    ports: ["80:80"]
    x-orcinus-expose: ingress          # ingress | nodeport | loadbalancer | clusterip
    x-orcinus-host: web.local
  db:
    image: postgres:16
    x-orcinus-controller: statefulset  # deployment | statefulset | daemonset
    x-orcinus-volume-size: 5Gi
    x-orcinus-secret: [POSTGRES_PASSWORD]
```

---

## 8. Licensing & Attribution

- Orcinus is licensed under the **MIT License** (see `LICENSE`).
- The vendored kompose fork (`third_party/kompose`) remains under **Apache-2.0**;
  its license header is retained and it is attributed in `NOTICE`. This is a
  requirement of reusing that code, independent of orcinus's own MIT license.

---

## 9. Phased Roadmap

**M0 — Foundation (scaffold). ✅** Multicall CLI, command tree, `--help`.

**M1 — Compose convert. ✅** `orcinus deploy --dry-run [-o dir]` produces valid
manifests with ownership labels and `x-orcinus-*` support. Unit + offline e2e.

**M2 — Deploy to a cluster. ✅** `pkg/deploy` server-side apply + ownership-based
prune + `--wait`; `orcinus deploy` and `orcinus rm` work against a kubeconfig.
Covered by a live single-node e2e that boots a real cluster in a container.

**M3 — Cluster runtime. ✅** `orcinus cluster init` provisions a single-node cluster and
writes `~/.orcinus/kubeconfig`; `orcinus cluster join` adds nodes (reads saved cluster
state). Implemented as a runtime-manager provider (container-backed; docker
command from `$ORCINUS_DOCKER`). Covered by a live cluster e2e (init → join(2
nodes) → deploy → ls → ps → rm). A fully in-binary embed remains a future option.

**M4 — Unified experience. ✅ (partial)** After `init`, all cluster commands use
the orcinus-managed kubeconfig automatically — no `--kubeconfig` needed. `deploy`
with no `-f` auto-discovers `orcinus.yml`/compose files.

**M5 — Hardening.** Broader compose coverage, multi-arch releases, more e2e.

---

## 10. Development Prerequisites

- **Go toolchain.** Everything builds with a standard Go toolchain (no CGO).
- **Cluster runtime (`init`/`join`)** needs a container runtime on the host
  (`$ORCINUS_DOCKER`, default `docker`). Conversion/deploy against an existing
  cluster need none of that.

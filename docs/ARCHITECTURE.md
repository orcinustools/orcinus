# Orcinus — Architecture

> **Compose-simple. Cluster-strong.** A lightweight cluster runtime that runs your
> `docker-compose.yml` files and Kubernetes manifests natively — no translation,
> no drama. One binary runs a cluster **and** deploys your compose files to it —
> no hand-written Kubernetes manifests required.
>
> **Language:** Go (no CGO). **License:** MIT (see [§8](#8-licensing--attribution)).
> **Strategy:** run a lightweight Kubernetes runtime (managed by a container
> provider by default, or embedded into a single binary), and **fork kompose** so
> compose conversion is fully Docker Compose compatible and entirely under
> orcinus's control.

---

## 1. Vision & Scope

Orcinus is one binary that combines two capabilities:

1. **Cluster runtime** — provisions and runs a lightweight Kubernetes control
   plane + agents (edge, dev, CI, homelab, bare-metal).
2. **Compose-native** — a user only needs a `docker-compose.yml`; `orcinus
   deploy` turns it into Kubernetes objects and applies them, no Kubernetes YAML
   by hand.

Target user experience:

```bash
orcinus cluster init                                    # start a single-node cluster
orcinus deploy -f docker-compose.yml                    # convert + apply in one shot
orcinus deploy -f docker-compose.yml --dry-run -o out   # convert to manifests only
orcinus rm myapp                                        # remove a project's resources
```

**Design goals:**
- Single binary, multicall (cluster lifecycle + compose/deploy + day-2 ops).
- Compose → Deployment/StatefulSet/DaemonSet/Service/PVC/ConfigMap/Secret/Ingress,
  plus HPA and Argo Rollouts, driven by `x-orcinus-*` hints.
- Idempotent `deploy` / `rm` via ownership labels, with prune and rollback.
- Zero-config defaults, everything overridable.

**Intentionally best-effort / out of scope:** compose features with no clean
Kubernetes equivalent (e.g. layered `depends_on` healthcheck ordering) are
approximated; `networks` are ignored in favor of flat cluster networking.

---

## 2. Design Principles

1. **Upstream where possible; fork only when full control is needed.** The
   cluster runtime is consumed as-is (bundled binary); **kompose is *forked***
   (`third_party/kompose`) because the conversion engine must follow the Docker
   Compose spec exactly and carry the `x-orcinus-*` mapping — actively maintained
   (periodic rebase), not a dead/rebranded fork.
2. **One binary, many faces (multicall).** The first subcommand selects the mode;
   the same binary can also *be* the runtime (`orcinus runtime …`) in the embedded
   build.
3. **Compose is a first-class citizen.** Conversion is integrated into the deploy
   flow, not a separate step.
4. **Idempotent, prune-able, rollback-able.** Every generated resource carries
   ownership labels (`app.kubernetes.io/managed-by=orcinus` + `orcinus.io/project`)
   so `deploy`/`rm` are safe to repeat, `--prune` removes what left the input, and
   `rollback` reverts to a prior revision.
5. **Zero-config, but overridable.** Sensible defaults; everything configurable
   via flags or compose annotations (`x-orcinus-*`).

---

## 3. The kompose Fork

Although kompose can be imported as a library, orcinus **forks** it
(`third_party/kompose`, wired via a `replace` directive):

- **Full Compose compatibility.** The fork's loader is anchored to
  `compose-spec/compose-go` (the official Docker Compose parser); a fork lets us
  close feature gaps directly instead of waiting on upstream.
- **Full control over the transformer.** The `x-orcinus-*` extensions ([§7](#7-the-x-orcinus-extensions)),
  ownership labels and mapping rules are woven into the conversion pipeline.
- **Iteration speed.** No waiting on upstream merges for mapping fixes.

**Obligations:** retain the fork's Apache-2.0 headers & attribution (`NOTICE`),
rebase periodically. **Wiring:** orcinus translates `x-orcinus-*` keys onto the
fork's native per-service labels, hands the files to the fork's loader →
transformer, then decorates the resulting objects (labels, namespace, HPA,
Rollout, strategy).

---

## 4. High-Level Architecture

```
                       ┌───────────────────────────────────────────────┐
   argv dispatch ────► │  cmd/orcinus  (multicall router, cobra)         │
                       │                                                 │
                       │  cluster init/join/status/down → pkg/cluster    │
                       │  deploy / rm                    → pkg/compose ─┐ │
                       │  ls / ps / logs / scale / autoscale / rollback │ │
                       │  secret / plugin / kubectl                     │ │
                       │  runtime (embedded build only)  → pkg/runtime  │ │
                       └───────────────────────────────────────────────┘ │
                              │              │                            │
              provider select │              │ client-go (SSA/prune)      │ fork
                    ┌─────────┴────────┐     ▼                            ▼
                    ▼                  ▼   ┌────────────────┐   ┌────────────────────┐
        ┌────────────────────┐ ┌──────────┴──────┐          │   │  pkg/compose       │
        │ docker provider    │ │ embedded provider│          │   │  (forked kompose)  │
        │ runtime in a       │ │ runtime native   │          │   │  loader→transform  │
        │ container          │ │ on the host      │          │   │  →decorate→objects │
        └─────────┬──────────┘ └────────┬─────────┘          │   └────────────────────┘
                  ▼                     ▼                     ▼
             ┌──────────────────────────────────┐   ┌────────────────────┐
             │  Kubernetes control plane + agent │◄──┤  Kubernetes API    │
             │  (the orcinus cluster)            │   │  (client-go)       │
             └──────────────────────────────────┘   └────────────────────┘
```

Flow of `orcinus deploy -f docker-compose.yml` (doc detected as compose):

```
compose file ─► pkg/compose.Convert()  (forked loader → transformer → []Object)
             ─► decorate                (managed-by/project labels, namespace, x-orcinus-*,
                                         HPA, Rollout, strategy)
             ─► pkg/deploy.Apply()      (server-side apply via client-go, prune, wait)
```

Auto-installed dependencies: if the input needs cert-manager (TLS) or Argo
Rollouts (progressive delivery), `deploy` installs the matching plugin first,
then applies with a **fresh** REST mapper so the new CRDs resolve.

---

## 5. Components

| Package | Responsibility |
|---|---|
| `cmd/orcinus` | Entry point, multicall router, cobra command tree |
| `pkg/compose` | Load compose (forked kompose), transform to k8s objects, decorate, `x-orcinus-*`, HPA, Rollout, strategy/update_config |
| `pkg/detect`  | Classify each YAML doc as compose vs raw manifest |
| `pkg/deploy`  | Server-side apply, ownership prune, wait/readiness, scale, autoscale, rollback, secrets, project listing |
| `pkg/cluster` | Cluster lifecycle (init/join/status/down); two runtime providers (`docker`, `embedded`); kubeconfig + state |
| `pkg/plugin`  | Built-in add-on catalog (ingress/TLS, storage, autoscale, rollouts, registry, dashboards) + profiles; install/upgrade/remove |
| `pkg/runtime` | The embedded runtime: `go:embed` the runtime binary (build tag `embedruntime`), extract + exec; stub otherwise |
| `pkg/version` | Build version, embedded component versions |

Command surface (all under `orcinus`): `cluster {init,join,status,down}`,
`deploy`, `rm`, `ls`, `ps`, `logs`, `scale`, `autoscale`, `rollback`, `secret`,
`plugin {install,list,upgrade,remove}`, `kubectl` (passthrough), and (embedded
build) the hidden `runtime` passthrough. See [USAGE.md §5](USAGE.md) for the full
reference.

---

## 6. Compose → Kubernetes Mapping (summary)

| Compose element | Kubernetes object | Notes |
|---|---|---|
| `service` | `Deployment` (default) | override via `x-orcinus-controller: statefulset/daemonset` |
| `ports` | `Service` (ClusterIP) | publish via `x-orcinus-expose: ingress/nodeport/loadbalancer` |
| `volumes` (named) | `PersistentVolumeClaim` | size via `x-orcinus-volume-size` |
| `environment` / `env_file` | `env` + `ConfigMap`/`Secret` | secrets marked with `x-orcinus-secret` |
| `deploy.replicas` | `.spec.replicas` | |
| `deploy.update_config` | `.spec.strategy` + minReadySeconds/progressDeadline | order/parallelism/delay/monitor mapped |
| `deploy.resources` | `resources.limits/requests` | cpu/memory mapped and unit-tested |
| `healthcheck` | `livenessProbe` | exec/http probe derived from the compose healthcheck |
| `x-orcinus-autoscale-*` | `HorizontalPodAutoscaler` | min/max/cpu/memory → HPA for the service |
| `x-orcinus-strategy` | `.spec.strategy` | rolling (default) / recreate + maxSurge/maxUnavailable |
| `x-orcinus-rollout` | Argo `Rollout` | canary / bluegreen (replaces the Deployment) |
| `restart` | `restartPolicy` / managed by controller | |
| `depends_on` | best-effort apply ordering | no complex readiness guarantee |
| `networks` | (ignored) | flat Kubernetes networking |

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
    x-orcinus-tls: true                # request a cert-manager certificate
  db:
    image: postgres:16
    x-orcinus-controller: statefulset  # deployment | statefulset | daemonset
    x-orcinus-volume-size: 5Gi
    x-orcinus-secret: [POSTGRES_PASSWORD]
```

Additional keys cover autoscaling (`x-orcinus-autoscale-{min,max,cpu,memory}`),
deployment strategy (`x-orcinus-strategy`, `x-orcinus-max-surge`,
`x-orcinus-max-unavailable`) and progressive delivery (`x-orcinus-rollout`). See
[USAGE.md](USAGE.md) and [DEPLOYMENT.md](DEPLOYMENT.md) for the full set.

---

## 8. Runtime Providers

`orcinus cluster init --runtime <docker|embedded>` selects how the cluster runs.

1. **`docker` (default).** Orcinus drives a container-based runtime (docker
   command from `$ORCINUS_DOCKER`). No special build, works anywhere a container
   runtime runs; this is the fully-tested default path. `cluster init` also
   best-effort enables the bundled metrics-server so HPAs get metrics.

2. **`embedded` (opt-in build).** The runtime is bundled into the orcinus binary
   via `go:embed` (`pkg/runtime`) and run **natively on the host as a managed
   process** — no container runtime, a single self-contained binary. It is
   compiled only into the binary built with `make orcinus-embedded` (build tag
   `embedruntime`); the default binary returns a clear "not compiled in" error, so
   it stays lean. The same binary can also *be* the runtime via the hidden
   `orcinus runtime …` passthrough.

   Validated end-to-end on a real host: `init --runtime embedded` → node Ready →
   workload Running → `cluster down` reaps the server, its containerd shims and
   mounts with no residue; `orcinus kubectl` routes through the built-in kubectl.
   It needs **root** and a **real host** with cgroup delegation (systemd-style);
   running it *nested inside another container* hits a cgroup-v2 delegation limit
   that does not occur on a real host. A true in-binary *library import* (no exec)
   remains the heavy, unchosen alternative.

See [CLUSTER.md → Runtime providers](CLUSTER.md#runtime-providers) for usage.

---

## 9. Licensing & Attribution

- Orcinus is licensed under the **MIT License** (see `LICENSE`).
- The vendored kompose fork (`third_party/kompose`) remains under **Apache-2.0**;
  its license header is retained and it is attributed in `NOTICE` — a requirement
  of reusing that code, independent of orcinus's own MIT license.

---

## 10. Building & Prerequisites

- **Build.** Standard Go toolchain, no CGO. `make build` produces the default
  `orcinus`; `make orcinus-embedded` produces the single self-contained binary
  with the runtime built in (downloads the runtime asset once via `make
  runtime-asset`).
- **Running a cluster.** The `docker` provider needs a container runtime on the
  host (`$ORCINUS_DOCKER`, default `docker`). The `embedded` provider needs root
  and a real host, but **no** container runtime.
- **Deploying only.** Conversion and `deploy` against an existing cluster need
  neither — just a kubeconfig.
- **Testing.** `make test` (unit + offline conversion e2e); `make e2e-live` (boots
  a real cluster in a container); `make e2e-embed` (embed+exec runtime as PID 1);
  `make e2e-tls` (Ingress + Let's Encrypt against a real domain).

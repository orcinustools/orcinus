# Orcinus — Architecture & Plan

> A lightweight Kubernetes distribution (based on k3s) that *natively* understands
> `docker-compose.yml` (the conversion logic from kompose is embedded).
>
> **Status:** architecture draft v0.2 — open decisions finalized; core code in progress (see §10).
> **Implementation language:** Go.
> **Strategy:** a new Go project that **imports k3s & kubectl as a *library***,
> but **forks kompose** so that conversion is fully compatible with the Docker
> Compose specification and entirely under orcinus's control.

---

## 1. Vision & Scope

Orcinus is a single binary that combines two capabilities:

1. **Cluster runtime** like k3s — runs a lightweight Kubernetes control plane +
   agent (suitable for edge, dev, CI, homelab).
2. **Compose-native** like kompose — a user only needs a `docker-compose.yml`,
   then `orcinus deploy` turns it into Kubernetes objects and deploys them to the
   cluster without having to write k8s YAML manifests by hand.

Target user experience:

```bash
# Start a single-node cluster (control plane)
orcinus init

# Deploy straight from compose or a manifest (auto-detected)
orcinus deploy -f docker-compose.yml                    # convert + create in one shot
orcinus deploy -f docker-compose.yml --dry-run -o out   # convert to manifests only
orcinus rm myapp                                         # remove a project's resources
orcinus kubectl get pods                                # built-in kubectl (multicall)
```

> The final CLI verbs (`init`, `deploy`, `rm`, etc.) are defined in `CLI.md`.
> The old draft's `convert`/`up`/`down` have been merged into `deploy`/`rm`.

**In scope (v1):**
- Single binary, multicall (server, agent, kubectl, and the compose subcommands).
- Compose → Deployment/Service/PVC/ConfigMap/Secret/Ingress conversion.
- Idempotent `deploy` / `rm` via ownership labels.

**Out of scope (v1):**
- Compose orchestration features with no k8s equivalent (e.g. complex
  `depends_on` with layered healthchecks) — approached best-effort.
- Multi-cluster / HA control plane (later, following upstream k3s capabilities).

---

## 2. Design Principles

1. **Upstream as a library where possible; fork only when full control is needed.**
   k3s & kubectl are **imported** as public packages so we can ride upstream
   releases without a large maintenance burden. **kompose is *forked*** because we
   need full control over the conversion engine to truly follow the Docker Compose
   specification (see §3.2) — this fork is actively maintained (periodic rebase to
   upstream), not a dead/rebranded fork.
2. **One binary, many faces (multicall).** The `argv[0]` name / first subcommand
   selects the mode — the same pattern k3s uses (`k3s server`, `k3s kubectl`, …).
3. **Compose is a first-class citizen.** Conversion is not a separate step; it is
   integrated into the deploy flow (`deploy`).
4. **Idempotent & rollback-able.** All compose-produced resources get ownership
   labels `app.kubernetes.io/managed-by=orcinus` + a project hash so `deploy`/`rm`
   are safe to repeat.
5. **Zero-config, but overridable.** Sensible defaults; everything configurable via
   flags / compose annotations (`x-orcinus-*`).

---

## 3. Technical Feasibility: using k3s & kompose

This section matters because it drives the entire strategy.

### 3.1 k3s as a library — **feasible, with precedent**
- k3s exposes packages such as `github.com/k3s-io/k3s/pkg/cli/cmds`,
  `.../pkg/server`, `.../pkg/agent`. Other distributions (e.g. **RKE2**) are built
  by **importing k3s packages** and adding their own layer — proof that the
  "k3s as a library" pattern works.
- **Build consequences:** k3s embeds a *fork* of Kubernetes (`k3s-io/kubernetes`)
  via `replace` directives in `go.mod`, plus embedded components (containerd, CNI,
  etc.). So orcinus's `go.mod` must **mirror k3s's `replace` block** to keep the
  Kubernetes versions aligned. The build likely needs specific tags & CGO.
- **Decision (D1) — FINAL: import as a library (a).** We import k3s packages
  directly into the orcinus binary (RKE2-style), not wrap the binary. More "native"
  & integrated; the build consequences (replace directives, tags, CGO) are accepted
  and isolated to M3. kubectl is included via the k3s multicall (same package set).

### 3.2 kompose — **FINAL: fork, not import**
- **Decision (D4) — FINAL: fork kompose.** Although kompose can be imported as a
  library (`github.com/kubernetes/kompose/pkg/{app,loader,transformer,kobject}`),
  we **fork** it. Reasons:
  - **Full Compose compatibility.** kompose's loader sometimes lags the latest
    Docker Compose spec; with a fork we can align the loader to
    `compose-spec/compose-go` (the official Docker Compose parser) and close
    feature gaps directly.
  - **Full control over the transformer.** The `x-orcinus-*` extensions (§8),
    ownership labels, and mapping rules can be woven directly into the transformer
    instead of bolting overrides on afterward — cleaner and more consistent.
  - **Iteration speed.** No waiting on upstream merges for mapping fixes.
- **Fork obligations:** actively maintained — periodic rebase/sync to upstream
  kompose, retain Apache-2.0 license headers & attribution (§9). The fork lives at
  `github.com/<org>/kompose` (or `internal/kompose` when vendored).
- **Usage flow:** `loader/compose` (forked, anchored to compose-go) →
  `kobject.KomposeObject` → `transformer/kubernetes` (forked, `x-orcinus-*` aware)
  → k8s objects. No shell-out.

> **Implementation note (M1):** the initial converter is built directly on
> `compose-spec/compose-go` plus an orcinus-owned transformer — this *is* the
> greenfield of the forked transformer described above, and keeps the M0–M2
> dependency set small (no client-go / k3s pull-in yet).

---

## 4. High-Level Architecture

```
                         ┌──────────────────────────────────────┐
                         │              orcinus (1 binary)        │
                         │                                        │
  argv dispatch ───────► │  cmd/orcinus  (multicall router)       │
                         │   ├─ init     → pkg/cluster (k3s server)│
                         │   ├─ join     → pkg/cluster (k3s agent) │
                         │   ├─ kubectl  → k3s multicall (kubectl) │
                         │   ├─ deploy   ┐                         │
                         │   │           ├→ pkg/compose (fork)     │
                         │   └─ rm       ┘   + pkg/deploy (apply)  │
                         └──────────────────────────────────────┘
                                     │                    │
                        embed/import │                    │ client-go
                                     ▼                    ▼
                    ┌────────────────────────┐   ┌───────────────────┐
                    │ k3s server/agent (lib) │   │  Kubernetes API    │
                    │  → embedded k8s + etcd │◄──┤  (orcinus cluster) │
                    │    containerd + CNI    │   └───────────────────┘
                    └────────────────────────┘
```

Flow of `orcinus deploy -f docker-compose.yml` (document detected as compose):

```
compose file ─► pkg/compose.Load()        (forked loader, anchored to compose-go)
             ─► pkg/compose.Transform()    (forked transformer → []client.Object)
             ─► pkg/compose.Decorate()     (managed-by labels, namespace, x-orcinus-*)
             ─► pkg/deploy.Apply()         (server-side apply via client-go)
             ─► persist state/ownership    (labels + optional "release" configmap)
```

---

## 5. Components

| Package | Responsibility | Primary source |
|---|---|---|
| `cmd/orcinus` | Entry point, multicall router, cobra wiring | new |
| `pkg/compose` | Load compose, transform to k8s objects, decorate | kompose (**fork**) |
| `pkg/detect`  | Classify each YAML doc as compose vs k8s manifest | new |
| `pkg/deploy`  | Apply/prune objects to the cluster, ownership & diff | client-go (new) |
| `pkg/cluster` | Start/stop server & agent, cluster configuration | k3s (import) |
| `pkg/config`  | Flag/env parsing, `x-orcinus-*` extensions | new |
| `pkg/version` | Build version, embedded k8s/k3s/kompose versions | new |

---

## 6. Repository Layout

```
orcinus/
├── ARCHITECTURE.md          # this document
├── CLI.md                   # CLI specification
├── README.md
├── LICENSE                  # Apache-2.0 (compatible with k3s & kompose)
├── NOTICE                   # upstream attribution (REQUIRED, see §9)
├── go.mod / go.sum          # mirrors k3s's `replace` block (once M3 lands)
├── Makefile                 # build multicall, tags, CGO, lint, test
├── cmd/
│   └── orcinus/
│       └── main.go          # multicall dispatch
├── pkg/
│   ├── compose/             # loader + transformer + decorate + extensions
│   ├── detect/              # compose vs manifest classifier
│   ├── deploy/              # apply/prune (M2)
│   ├── cluster/             # k3s runtime (M3)
│   ├── config/
│   └── version/
├── internal/                # private utilities
├── examples/
│   └── docker-compose.yml
├── test/
│   └── e2e/                 # end-to-end tests (build binary + run)
└── docs/
    ├── compose-mapping.md   # compose→k8s mapping table
    └── x-orcinus.md         # annotation extensions
```

---

## 7. Compose → Kubernetes Mapping (summary)

| Compose element | k8s object | Notes |
|---|---|---|
| `service` | `Deployment` (default) | override via `x-orcinus-controller: statefulset/daemonset` |
| `ports` | `Service` (ClusterIP) | publish via `x-orcinus-expose: ingress/nodeport` |
| `volumes` (named) | `PersistentVolumeClaim` | size via `x-orcinus-volume-size`; cluster default storage class |
| `environment` / `env_file` | `env` + `ConfigMap`/`Secret` | secrets marked with `x-orcinus-secret` |
| `deploy.replicas` | `.spec.replicas` | |
| `deploy.resources` | `resources.limits/requests` | |
| `restart` | `restartPolicy` / managed by Deployment | |
| `depends_on` | best-effort apply ordering | no complex readiness guarantee |
| `networks` | (ignored in v1) | flat k8s networking |

Full details → `docs/compose-mapping.md`. Most of these rules are owned by the
forked transformer; on top of them we add the `x-orcinus-*` layer and ownership.

---

## 8. The `x-orcinus-*` Extensions (the main mechanism)

Orcinus uses `x-*` extension keys inside a service for k8s hints. Compose ignores
`x-*` keys, so orcinus **parses them itself** and applies them during conversion.
Benefits: it does not pollute k8s object label metadata, and the schema is fully
under orcinus's control.

```yaml
services:
  web:
    image: nginx:1.27
    ports: ["80:80"]
    x-orcinus-expose: ingress          # create an Ingress, not just a ClusterIP
    x-orcinus-host: web.local
  db:
    image: postgres:16
    x-orcinus-controller: statefulset  # deployment | daemonset | statefulset
    x-orcinus-volume-size: 5Gi
    x-orcinus-secret: [POSTGRES_PASSWORD]
```

**Consequence (with the fork, §3.2):** because kompose is forked, reading
`x-orcinus-*` can be woven **directly into the transformer** — not just an external
override. `Decorate()` (§4) still exists for ownership labels & namespaces, but
service-specific hints are processed inside the conversion pipeline, giving more
consistent results than patching finished objects from the outside.

---

## 9. Licensing & Attribution (mandatory)

- k3s: **Apache-2.0**. kompose: **Apache-2.0**. Both compatible.
- Orcinus is licensed under **Apache-2.0**.
- We **MUST** ship a `NOTICE` file attributing k3s (k3s-io) and kompose
  (Kubernetes/CNCF), and preserve the license headers of vendored packages. This
  is a legal Apache-2.0 obligation, not optional.
- **k3s & kubectl** are **imported as dependencies** → light attribution burden.
- **kompose is *forked*** → heavier attribution burden but still reasonable as long
  as Apache-2.0 is honored: keep the license header on every forked file, list
  kompose (Kubernetes/CNCF) attribution in `NOTICE`, and mark our changes
  (Apache-2.0 §4: *state changes*). The fork does not claim upstream work as ours.

---

## 10. Phased Roadmap

**M0 — Foundation (scaffold). ✅**
`go.mod`, repo layout, `cmd/orcinus` multicall skeleton with cobra, subcommands
(`init`, `join`, `deploy`, `rm`, `ls`, `ps`, `logs`, `kubectl`, `version`),
Makefile, README, LICENSE, NOTICE. → green build, `--help` works.

**M1 — Compose convert (fastest value). ✅**
Build the converter on compose-go + an orcinus transformer: `orcinus deploy
--dry-run -o out -f ...` produces valid k8s manifests. Add ownership-label
decoration & basic `x-orcinus-*` extensions. *No cluster required* → easy to test.
Covered by unit tests + an e2e test that builds the binary and asserts output.

**M2 — Deploy to a cluster.**
`pkg/deploy` with client-go server-side apply + ownership-based prune.
`orcinus deploy` / `orcinus rm` work against an existing kubeconfig.

**M3 — Cluster runtime (k3s).**
Integrate k3s as a library (D1=import): `orcinus init` / `join` / `kubectl`.
The heaviest part (embedded k8s build). **Requires a Linux host with root + a
container runtime** — see §12.

**M4 — Unified experience.**
`orcinus deploy` with no external cluster: automatically uses the local orcinus
cluster, an internal kubeconfig, and a one-command "compose → running" experience.

**M5 — Hardening.**
End-to-end tests against a live cluster, documentation, multi-arch binary
releases, broader compose mapping coverage.

---

## 11. Risks & Open Decisions

| ID | Issue | Decision / Recommendation |
|---|---|---|
| D1 | k3s: import library vs wrap binary | **FINAL: import** (RKE2-style) |
| D4 | kompose: import vs fork | **FINAL: fork** (Compose compatibility + full control) |
| D2 | Sync `replace` in go.mod with k3s | Automate via a script that reads k3s's go.mod per version |
| R1 | Embedded k8s build is heavy (CGO, tags, time) | Isolate to M3; CI with cache; document prerequisites |
| R2 | client-go version conflict between k3s & the kompose fork | Pin versions; align the fork to k3s's client-go; test in M2 before M3 |
| R3 | Compose features with no k8s equivalent | Document limits; best-effort + warning |
| R4 | Maintenance burden of the kompose fork (upstream rebase) | Rebase periodically; keep the diff minimal & isolated; CI over the fork |

---

## 12. Development Prerequisites

- **Go toolchain.** M0–M2 build with a standard Go toolchain (no CGO). This repo
  was bootstrapped with a user-local Go SDK; see the `Makefile`.
- **M3 (embedded k3s)** additionally needs: a C toolchain (CGO), Linux, and a long
  build time. **Running** a single-node cluster for a live e2e test needs **root
  privileges + a container runtime** (k3s manages cgroups, iptables, containerd,
  CNI). M0–M2 need none of that and are fully testable without a cluster.

---

## 13. Next Steps

Open decisions (D1, D4, datastore, CLI verbs) are finalized, and M0–M1 are
implemented and tested (see §10). Remaining work:
1. **M2** — client-go apply/prune against an existing kubeconfig.
2. **M3** — embed k3s (needs a privileged Linux host); wire up `init`/`join`.
3. **M4/M5** — unified experience, live-cluster e2e, releases.

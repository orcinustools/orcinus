# Orcinus — Deployment & Update Strategies

How orcinus rolls out and updates your workloads: the default rolling update,
Swarm-native `deploy.update_config`, `recreate`, and progressive delivery
(canary / blue-green). Scaling & autoscaling are covered at the end.

Reference for all keys: [`USAGE.md`](./USAGE.md). Plugins: [`PLUGINS.md`](./PLUGINS.md).

Runnable examples: [`examples/deploy-strategies`](../examples/deploy-strategies/orcinus.yml)
(update_config + recreate), [`examples/autoscale`](../examples/autoscale/orcinus.yml),
[`examples/rollout`](../examples/rollout/orcinus.yml) (canary/blue-green).

---

## Table of Contents

- [Overview](#overview)
- [1. Rolling update (default)](#1-rolling-update-default)
- [2. Swarm-native `deploy.update_config`](#2-swarm-native-deployupdate_config)
- [3. `x-orcinus-strategy` (rolling / recreate)](#3-x-orcinus-strategy-rolling--recreate)
- [4. Progressive delivery — canary & blue-green](#4-progressive-delivery--canary--blue-green)
- [5. Scaling & autoscaling](#5-scaling--autoscaling)
- [Choosing a strategy](#choosing-a-strategy)
- [What is not mapped](#what-is-not-mapped)

---

## Overview

Each compose `service` becomes a Kubernetes **Deployment** by default (or a
StatefulSet/DaemonSet via `x-orcinus-controller`, or an **Argo Rollout** via
`x-orcinus-rollout`). How a new version replaces the old one is the *update
strategy*:

| Want | Use |
|---|---|
| Zero-downtime gradual replace (default) | nothing — rolling update |
| Tune the rollout (batch size, order, timing) | `deploy.update_config` |
| Stop-all-then-start (no two versions at once) | `x-orcinus-strategy: recreate` |
| Canary (shift traffic in steps) | `x-orcinus-rollout: canary` |
| Blue-green (flip after the new set is up) | `x-orcinus-rollout: bluegreen` |

---

## 1. Rolling update (default)

With no configuration, a service becomes a Deployment with Kubernetes'
`RollingUpdate` strategy: new pods are created and old ones retired gradually, so
there is no downtime.

```yaml
services:
  web:
    image: myapp:2
    ports: ["80"]
```

---

## 2. Swarm-native `deploy.update_config`

Orcinus reads the standard compose
[`deploy.update_config`](https://docs.docker.com/reference/compose-file/deploy/#update_config)
and maps it onto the Kubernetes rolling update — so a Swarm compose file behaves
as you'd expect.

| `update_config` | → Kubernetes |
|---|---|
| `order: start-first` | `maxSurge = parallelism`, `maxUnavailable = 0` (start new before stopping old) |
| `order: stop-first` (default) | `maxSurge = 0`, `maxUnavailable = parallelism` |
| `parallelism` | the count for the active knob (default 1) |
| `delay` | `minReadySeconds` |
| `monitor` | `progressDeadlineSeconds` |

```yaml
services:
  web:
    image: myapp:2
    ports: ["80"]
    deploy:
      update_config:
        order: start-first     # never dip below capacity
        parallelism: 2
        delay: 10s
        monitor: 60s
```

---

## 3. `x-orcinus-strategy` (rolling / recreate)

For direct control (and to select `recreate`), use the orcinus keys. They
**override** `deploy.update_config` when both are present.

| Key | Values | Effect |
|---|---|---|
| `x-orcinus-strategy` | `rolling` \| `recreate` | Update strategy (default rolling) |
| `x-orcinus-max-surge` | int or % | Rolling: extra pods during an update |
| `x-orcinus-max-unavailable` | int or % | Rolling: pods that may be down |

```yaml
services:
  web:                              # fine-tuned rolling
    image: myapp:2
    x-orcinus-strategy: rolling
    x-orcinus-max-surge: "25%"
    x-orcinus-max-unavailable: "0"
  migrator:                         # recreate: brief downtime, never two versions
    image: myapp:2
    x-orcinus-strategy: recreate
```

**Recreate** stops all old pods before starting new ones — use it when two
versions must not run simultaneously (e.g. an exclusive DB migration, or a
single-writer volume).

---

## 4. Progressive delivery — canary & blue-green

Plain Deployments can't do canary or blue-green. Set `x-orcinus-rollout` and
orcinus emits an **Argo Rollout** instead of a Deployment, **auto-installing the
`argo-rollouts` plugin** on first use.

```yaml
services:
  web:
    image: myapp:2
    ports: ["80"]
    x-orcinus-rollout: canary       # 50% → pause 15s → 100%
  api:
    image: myapp:2
    ports: ["8080"]
    x-orcinus-rollout: bluegreen    # bring up green, then flip the Service
```

- **canary** — shifts to the new version in steps (default: `setWeight 50` →
  `pause 15s` → 100%). Replica-based by default; add a service mesh/ingress for
  true traffic splitting.
- **blue-green** — brings the new version up alongside the old, then switches the
  service to it and retires the old (auto-promoted). Needs a `ports:` entry so
  there is a Service to flip.

Manage/observe rollouts:

```bash
kubectl get rollout                       # status: Progressing / Healthy
kubectl argo rollouts get rollout web     # if the Argo kubectl plugin is installed
```

An HPA declared via `x-orcinus-autoscale-*` automatically targets the Rollout.
Installing the controller manually (optional; deploy does it for you):

```bash
orcinus plugin install argo-rollouts
```

> On the **first** deploy of a service there is nothing to progress against, so
> the Rollout goes straight to Healthy. Canary/blue-green behavior kicks in on
> subsequent updates (new image).

---

## 5. Scaling & autoscaling

Independent of the update strategy:

```bash
orcinus scale web 3                              # set replicas
orcinus autoscale web --min 2 --max 8 --cpu 70   # HPA on CPU
```

Or declaratively in compose (needs resource **requests** for %-based targets;
orcinus clusters ship a working metrics-server):

```yaml
services:
  web:
    image: myapp:2
    deploy:
      resources:
        reservations: { cpus: "0.1", memory: 64M }
    x-orcinus-autoscale-min: 2
    x-orcinus-autoscale-max: 8
    x-orcinus-autoscale-cpu: 70
```

See [`USAGE.md`](./USAGE.md#511-orcinus-autoscale) for the full flag reference.

---

## Choosing a strategy

| Situation | Recommended |
|---|---|
| Stateless web/API, want zero downtime | rolling (default), tune with `update_config` |
| Can't run two versions at once | `x-orcinus-strategy: recreate` |
| Want to validate a release on real traffic gradually | `x-orcinus-rollout: canary` |
| Want an instant, reversible switch with a warm standby | `x-orcinus-rollout: bluegreen` |
| Need capacity to never dip during updates | rolling with `order: start-first` / `max-unavailable: 0` |

---

## What is not mapped

- `update_config.failure_action: rollback` — Kubernetes Deployments don't
  auto-roll-back a failed update (a failed rollout is *marked* failed after
  `progressDeadlineSeconds`, but not reverted). Roll back manually or use Argo
  Rollouts' analysis/abort features.
- `update_config.max_failure_ratio` — no direct equivalent.
- `rollback_config` — not mapped yet.
- True canary **traffic splitting** needs an ingress/mesh integration; the
  built-in canary is replica-weighted.

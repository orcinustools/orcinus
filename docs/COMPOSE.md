# Orcinus — Docker Compose & Swarm Compatibility

Orcinus deploys your `docker-compose.yml` **as-is** — it converts each service to
Kubernetes objects (via a forked kompose), and understands Swarm's `deploy:` keys.
This page lists what maps, what's partial, and what isn't — so you know what to
expect. Kubernetes-only tweaks use [`x-orcinus-*`](./USAGE.md#appendix-b--x-orcinus--extension-reference).

Legend: ✅ supported · ⚠️ partial / best-effort · ❌ not mapped

---

## Service keys

| Compose key | Kubernetes | Status |
|---|---|---|
| `image` | container image | ✅ |
| `command` / `entrypoint` | container command/args | ✅ |
| `environment` / `env_file` | container `env` (move to a Secret with `x-orcinus-secret`) | ✅ |
| `ports` | `Service` (ClusterIP; `x-orcinus-expose` for ingress/nodeport/lb/headless) | ✅ |
| `volumes` (named) | `PersistentVolumeClaim` (size via `x-orcinus-volume-size`) | ✅ |
| `volumes` (bind mount) | `hostPath` (node-local) — see [Volumes](./USAGE.md#7-volumes--storage) | ✅ |
| `configs` | `ConfigMap` mounted at the target (relative `file:` supported) | ✅ |
| `secrets` | `Secret` mounted at the target (relative `file:` supported) | ✅ |
| `healthcheck` | `livenessProbe` (readiness via `kompose.service.healthcheck.readiness.*` labels) | ✅ |
| `restart` | pod `restartPolicy` | ✅ |
| `user` | `securityContext.runAsUser`/`runAsGroup` | ✅ |
| `working_dir` | container `workingDir` | ✅ |
| `cap_add` / `cap_drop` | `securityContext.capabilities` | ✅ |
| `privileged` | `securityContext.privileged` | ✅ |
| `read_only` | `securityContext.readOnlyRootFilesystem` | ✅ |
| `tmpfs` | `emptyDir` (memory-backed) | ✅ |
| `stop_grace_period` | `terminationGracePeriodSeconds` | ✅ |
| `labels` | object/pod labels | ✅ |
| `profiles` | service selection via `--profile` (see below) | ✅ |
| `depends_on` | best-effort apply ordering (no startup gating) | ⚠️ |
| `networks` | ignored — flat cluster networking (reach a service by name) | ❌ |
| `build` | not built — provide a pre-built `image:` (see `orcinus plugin install registry`) | ❌ |
| `links` / `extends` | not mapped | ❌ |

## `deploy:` keys (Swarm)

| Compose key | Kubernetes | Status |
|---|---|---|
| `deploy.mode` | `Deployment` (replicated) / `DaemonSet` (global) | ✅ |
| `deploy.replicas` | `.spec.replicas` | ✅ |
| `deploy.resources.limits/reservations` | container `resources` (cpu + memory) | ✅ |
| `deploy.resources…devices` (GPU) | GPU limit — `capabilities: [gpu]` → `nvidia.com/gpu` (see [GPUs](#gpus)) | ✅ |
| `deploy.resources…generic_resources` | extended-resource limit (a `gpu` kind → `nvidia.com/gpu`) | ✅ |
| `deploy.update_config` | `.spec.strategy` + minReadySeconds/progressDeadline | ✅ |
| `deploy.placement.constraints` | `nodeAffinity` (see [Placement](./USAGE.md#8-placement--node-constraints)) | ✅ |
| `deploy.placement.preferences` (spread) | `topologySpreadConstraints` | ✅ |
| `deploy.endpoint_mode` | `vip` → ClusterIP, `dnsrr` → headless Service | ✅ |
| `deploy.labels` | workload labels | ✅ |
| `deploy.restart_policy.condition` | pod `restartPolicy` | ✅ |
| `deploy.restart_policy` (delay/max_attempts/window) | no direct equivalent (k8s uses crash-backoff) | ⚠️ |
| `deploy.rollback_config` | not mapped — use `orcinus rollback`, or `x-orcinus-rollout` for progressive delivery | ❌ |

## Profiles

Services with a `profiles:` list are only deployed when you request the profile:

```bash
orcinus deploy                       # services with no profile
orcinus deploy --profile debug       # also services in the "debug" profile
```

Services without `profiles:` are always deployed.

## Configs & secrets

```yaml
services:
  app:
    image: myapp:1.0
    configs:
      - source: appconf
        target: /etc/app/config.yaml   # → ConfigMap mounted here
    secrets:
      - source: apikey
        target: apikey                 # → Secret mounted at /run/secrets/apikey
configs:
  appconf:
    file: ./config.yaml                # relative paths resolve to the compose file's dir
secrets:
  apikey:
    file: ./secrets/api.key
```

Relative `file:` paths resolve against the compose file's directory (deploy from
the project dir, or pass an absolute path). For a private-registry login use
[`orcinus secret create-registry`](./REGISTRY.md), not a compose secret.

## GPUs

Both the modern Compose GPU form and the older Swarm form map to a Kubernetes GPU
limit:

```yaml
services:
  trainer:
    image: my/cuda-app
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia        # amd → amd.com/gpu
              count: 1              # "all" defaults to 1 (set an explicit number)
              capabilities: [gpu]
```

→ the container gets `resources.limits: { nvidia.com/gpu: "1" }`.

**To actually schedule on GPUs the cluster must advertise them** — install the
device plugin and have NVIDIA drivers + the NVIDIA container runtime on the GPU
nodes:

```bash
orcinus plugin install nvidia-device-plugin
```

Without a GPU node advertising `nvidia.com/gpu`, a GPU pod stays **Pending**
(`orcinus ps <project>`). The old Swarm form also works:
`generic_resources: [{ discrete_resource_spec: { kind: gpu, value: 1 } }]`.

## Notes

- **Not sure what a file produces?** `orcinus deploy -f orcinus.yml --dry-run`
  prints the exact Kubernetes YAML.
- **Anything unmapped** can be added as a raw Kubernetes manifest in the same file
  (orcinus classifies each YAML document independently and applies manifests as-is).
- **Kubernetes-only features** (ingress/TLS, autoscaling, rollouts, node pinning,
  image-pull secrets) use `x-orcinus-*` — see
  [USAGE Appendix B](./USAGE.md#appendix-b--x-orcinus--extension-reference).

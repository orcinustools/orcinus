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
| `environment` | container `env` (move to a Secret with `x-orcinus-secret`) | ✅ |
| `env_file` | `ConfigMap` + `envFrom` — see [Environment files](#environment-files) | ✅ |
| `ports` | `Service` (ClusterIP; `x-orcinus-expose` for ingress/nodeport/lb/headless) | ✅ |
| `volumes` (named) | `PersistentVolumeClaim` (size via `x-orcinus-volume-size`) | ✅ |
| `volumes` (bind mount) | `hostPath` (node-local) — see [Volumes](./USAGE.md#7-volumes--storage) | ✅ |
| `configs` | `ConfigMap` mounted at the target (relative `file:` supported) | ✅ |
| `secrets` | `Secret` mounted at the target (relative `file:`, or `external: true` for an existing one) | ✅ |
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

## Environment files

Each `env_file:` becomes a ConfigMap the container pulls in with `envFrom`. The
ConfigMap is named after the file, so `.env` → `env` and `config/db.env` →
`config-db-env`.

```yaml
services:
  app:
    image: myapp:1.0
    env_file:
      - .env                 # → ConfigMap "env"
      - ./config/db.env      # → ConfigMap "config-db-env"
      - path: ./local.env    # long form
        required: false      # skipped when the file is absent
    x-orcinus-secret:
      - DB_PASSWORD          # moved out of the ConfigMap into a Secret
```

Paths resolve against the compose file's directory, the same as `configs:` and
`secrets:`. A missing file fails the deploy naming the service and the path,
unless the long form marks it `required: false`.

Keys listed in `x-orcinus-secret` are pulled out of the generated ConfigMap and
into a `<service>-secret` Secret, with the container reading them through a
`secretKeyRef`. Since the ConfigMap is shared by every service using that env
file, marking a key secret removes it for all of them.

A `.env` sitting next to the compose file is also used for `${VAR}`
interpolation within the compose document itself, as in Docker Compose.

## Using a Secret that already exists

For credentials you do not want in the compose file, create the Secret once and
reference it. `orcinus secret ls` shows what the cluster already has.

```bash
orcinus secret create app-secret --from-literal DB_PASS=xxx --from-literal API_KEY=yyy
```

**As environment variables** — every key in the Secret becomes an env var under
its own name:

```yaml
services:
  app:
    image: myapp:1.0
    x-orcinus-env-from-secret: app-secret   # or a list: [app-secret, extra-secret]
```

This is appended after any `env_file:` ConfigMap, so a key in both takes the
Secret's value. Nothing is generated for the Secret — it has to exist at deploy
time, or the pod stays pending on a missing reference.

**As a mounted file** — use compose's own `secrets:` with `external: true`:

```yaml
services:
  app:
    image: myapp:1.0
    secrets:
      - source: app-secret
        target: /etc/app/secret.env   # absolute target → predictable path
secrets:
  app-secret:
    external: true                    # use the cluster's, do not create one
```

Two constraints here that `x-orcinus-env-from-secret` does not have: the key in
the `secrets:` map must match the Secret's name in the cluster (a `name:` field
is ignored), and the Secret must contain a data key of that same name —
`target:` renames the file, not the key. So this route suits a Secret holding a
single value; for one holding several keys, prefer the env form above.

Deploying with a `--dry-run` prints the generated `envFrom`/volume, which is the
quickest way to confirm the wiring before it reaches the cluster.

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

### Importing a file into a Secret

`secrets: { <name>: { file: ./path } }` is the declarative way to get a file's
contents into a Secret — the CLI's `--from-file` without leaving the compose
file. The bytes are taken verbatim, trailing newline included.

**Creating the Secret and consuming it are separate steps.** The top-level
`secrets:` entry creates it; how a service uses it is up to the service. Listing
it under a service's `secrets:` mounts it as a file, `x-orcinus-env-from-secret`
loads it as environment variables, and you can do both, or neither:

```yaml
services:
  app:
    image: myapp:1.0
    x-orcinus-env-from-secret: apikey   # → env var, no volume at all
    secrets:                            # → and/or mounted as a file
      - source: apikey
        target: /etc/app/api.key
secrets:
  apikey:
    file: ./api.key                     # creates the Secret either way
```

Two things to know before you rely on it:

- **The data key is always the secret's name**, whatever the file is called. A
  `name:` field is accepted by the parser and then ignored. Since
  `x-orcinus-env-from-secret` turns each key into a variable of the same name,
  `apikey: {file: ./api.key}` arrives in the container as `apikey=<contents>`.
  To choose the variable name, create the Secret with
  `orcinus secret create app-secret --from-file API_KEY=./api.key` and reference
  it by name instead.
- **`environment:` as a secret source is not supported.**
  `secrets: { x: { environment: VAR } }` parses, then is silently skipped — no
  Secret is created and nothing is mounted. (`content:` is rejected outright by
  the schema, so at least that one tells you.)

See [USAGE §5.13](./USAGE.md#513-orcinus-secret) for the CLI flag, and
[Using a Secret that already exists](#using-a-secret-that-already-exists) above
for referencing one this file did not create.

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

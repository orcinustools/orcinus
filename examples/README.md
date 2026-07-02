# Orcinus Examples

Each folder has an `orcinus.yml` you can deploy with:

```bash
cd examples/<folder>
orcinus deploy --wait          # picks up orcinus.yml automatically
```

| Example | What it shows |
|---|---|
| [`wordpress/`](./wordpress/orcinus.yml) | Fullstack **WordPress + MariaDB** — StatefulSet DB, PVCs, secrets, Ingress. Pure compose with `x-orcinus-*`. |
| [`postgres-operator/`](./postgres-operator/orcinus.yml) | **HA Postgres via an operator** (CloudNativePG) — applies the operator's `Cluster` custom resource. |
| [`traefik-letsencrypt/`](./traefik-letsencrypt/orcinus.yml) | Web app **exposed through Traefik with a Let's Encrypt cert** (cert-manager). Mixes a compose service with raw k8s manifests in one file. |
| [`redis/`](./redis/orcinus.yml) | **Redis** with persistence — StatefulSet + PVC + Service. Reachable at `redis:6379`. |
| [`monitoring/`](./monitoring/orcinus.yml) | **Prometheus + Grafana** stack — PVCs, Grafana admin password in a Secret, both exposed via Ingress. |
| [`app-with-cnpg/`](./app-with-cnpg/orcinus.yml) | A web app (**Adminer**) connecting to the CNPG Postgres cluster from `postgres-operator/`, consuming the operator-generated Secret via `envFrom`. |
| [`ingress-tls/`](./ingress-tls/orcinus.yml) | Web app on a **public domain with automatic Let's Encrypt HTTPS** — `x-orcinus-expose: ingress` + `x-orcinus-host` + `x-orcinus-tls`. See [`../docs/PLUGINS.md`](../docs/PLUGINS.md). |
| [`deploy-strategies/`](./deploy-strategies/orcinus.yml) | **Update strategies**: Swarm `deploy.update_config` (rolling) + `x-orcinus-strategy: recreate`. See [`../docs/DEPLOYMENT.md`](../docs/DEPLOYMENT.md). |
| [`autoscale/`](./autoscale/orcinus.yml) | **Horizontal autoscaling** via `x-orcinus-autoscale-*` (HPA on CPU). |
| [`rollout/`](./rollout/orcinus.yml) | **Progressive delivery** — `x-orcinus-rollout: canary` / `bluegreen` (Argo Rollouts, auto-installed). |
| [`custom-cert/`](./custom-cert/orcinus.yml) | **Bring-your-own TLS cert** (incl. wildcard) via `orcinus secret create-tls` + `x-orcinus-tls-secret`. |

Single-file basics also live here: [`orcinus.yml`](./orcinus.yml) and
[`docker-compose.yml`](./docker-compose.yml).

Some examples need a prerequisite installed first (an operator or cert-manager) —
see the comments at the top of each file. To preview what a file produces without
deploying:

```bash
orcinus deploy -f orcinus.yml --dry-run
```

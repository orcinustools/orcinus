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

Single-file basics also live here: [`orcinus.yml`](./orcinus.yml) and
[`docker-compose.yml`](./docker-compose.yml).

Some examples need a prerequisite installed first (an operator or cert-manager) —
see the comments at the top of each file. To preview what a file produces without
deploying:

```bash
orcinus deploy -f orcinus.yml --dry-run
```

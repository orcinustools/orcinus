# Orcinus — HTTP API

`orcinus api` serves a REST API over the same engine the CLI uses, so anything
you can `orcinus deploy` you can also `POST /api/v1/deploy`. It ships an OpenAPI
spec and an interactive Swagger UI.

- **Interactive docs:** `GET /docs` (Swagger UI)
- **Spec:** `GET /openapi.json` (or `/openapi.yaml`)

---

## Table of Contents

- [1. Starting the server](#1-starting-the-server)
- [2. Authentication](#2-authentication)
- [3. Endpoints](#3-endpoints)
- [4. Deploy & convert](#4-deploy--convert)
- [5. Projects, scale, rollback](#5-projects-scale-rollback)
- [6. Secrets](#6-secrets)
- [7. Plugins](#7-plugins)
- [8. Cluster & system](#8-cluster--system)
- [9. Notes](#9-notes)

---

## 1. Starting the server

```bash
orcinus api --addr :8080 --token "$(openssl rand -hex 16)"
# → docs:    http://:8080/docs
#   openapi: http://:8080/openapi.json
```

| Flag | Default | Description |
|---|---|---|
| `--addr` | `:8080` | Listen address |
| `--token` | — | Bearer token for `/api/v1/*` (or `$ORCINUS_API_TOKEN`) |
| `--kubeconfig` | auto | Cluster to target (default: `~/.orcinus/kubeconfig`, `$KUBECONFIG`, `~/.kube/config`) |

The server resolves the cluster the same way the CLI does; run it wherever a
kubeconfig for your orcinus cluster is available. It shuts down gracefully on
SIGINT/SIGTERM.

## 2. Authentication

If `--token` (or `$ORCINUS_API_TOKEN`) is set, every `/api/v1/*` request must send
`Authorization: Bearer <token>`. The open routes — `/healthz`, `/version`,
`/openapi.json`, `/openapi.yaml`, `/docs` — never require it. If no token is set
the API is unauthenticated (the server prints a warning) — only do that on a
trusted network.

```bash
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/projects
```

## 3. Endpoints

| Method & path | Description |
|---|---|
| `GET /healthz` | Liveness |
| `GET /version` | Build & component versions |
| `POST /api/v1/convert` | Render input to manifests (no apply) |
| `POST /api/v1/deploy` | Convert + apply (auto-installs required plugins) |
| `GET /api/v1/projects` | List deployed projects |
| `GET /api/v1/projects/{project}/pods` | List a project's pods |
| `DELETE /api/v1/projects/{project}` | Remove a project's resources |
| `POST /api/v1/projects/{project}/services/{service}/scale` | Scale a service |
| `POST /api/v1/projects/{project}/services/{service}/rollback` | Roll back a service |
| `GET /api/v1/secrets` | List secrets |
| `POST /api/v1/secrets` | Create/update an opaque secret |
| `DELETE /api/v1/secrets/{name}` | Delete a secret |
| `GET /api/v1/plugins` | List plugins + install state |
| `POST /api/v1/plugins/{name}` | Install a plugin |
| `DELETE /api/v1/plugins/{name}` | Remove a plugin |
| `GET /api/v1/cluster` | Cluster status |

All examples below assume `TOKEN` and a server at `localhost:8080`.

## 4. Deploy & convert

`deploy` and `convert` accept **either** a JSON body **or** a raw compose/manifest
body (with options as query params).

**JSON body:**

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
        "source": "services:\n  web:\n    image: nginx:1.27\n    ports: [\"80\"]\n",
        "project": "shop",
        "wait": true
      }' \
  http://localhost:8080/api/v1/deploy
# → {"applied":3,"project":"shop","installed":[]}
```

**Raw YAML body** (options as query params) — convenient with a file:

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/yaml" \
  --data-binary @orcinus.yml \
  "http://localhost:8080/api/v1/deploy?project=shop&wait=true&prune=true"
```

`convert` returns the rendered manifests without touching the cluster:

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/yaml" \
  --data-binary @orcinus.yml \
  "http://localhost:8080/api/v1/convert?project=shop"
# → {"objects":3,"manifests":"apiVersion: apps/v1\nkind: Deployment\n..."}
```

`DeployRequest` fields: `source` (required), `project` (default `default`),
`namespace`, `mode` (`""`|`compose`|`manifest`), `replicas`, `pvcSize`, `prune`
(default `true`), `wait`, `acmeEmail`. Like the CLI, `deploy` auto-installs
cert-manager (for `x-orcinus-tls`, needs `acmeEmail`) and argo-rollouts (for
`x-orcinus-rollout`).

## 5. Projects, scale, rollback

```bash
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/projects
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/projects/shop/pods

# Scale service "web" of project "shop" to 3
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"replicas":3}' \
  http://localhost:8080/api/v1/projects/shop/services/web/scale

# Roll back "web" to its previous revision
curl -H "Authorization: Bearer $TOKEN" -X POST \
  http://localhost:8080/api/v1/projects/shop/services/web/rollback

# Remove the whole project
curl -H "Authorization: Bearer $TOKEN" -X DELETE \
  http://localhost:8080/api/v1/projects/shop
```

Add `?namespace=<ns>` where relevant (defaults to `default`).

## 6. Secrets

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"app-config","data":{"FOO":"bar","BAZ":"qux"}}' \
  http://localhost:8080/api/v1/secrets

curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/secrets
curl -H "Authorization: Bearer $TOKEN" -X DELETE http://localhost:8080/api/v1/secrets/app-config
```

## 7. Plugins

```bash
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/plugins

# Install cert-manager (options mirror the CLI plugin flags)
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"email":"you@example.com"}' \
  http://localhost:8080/api/v1/plugins/cert-manager

# Install object storage (MinIO, distributed)
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"provider":"minio","replicas":4,"size":"10Gi"}' \
  http://localhost:8080/api/v1/plugins/storage

curl -H "Authorization: Bearer $TOKEN" -X DELETE http://localhost:8080/api/v1/plugins/cert-manager
```

## 8. Cluster & system

```bash
curl http://localhost:8080/healthz         # {"status":"ok"}
curl http://localhost:8080/version
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/cluster
```

## 9. Notes

- **Errors** are JSON: `{"error":"..."}` with a matching HTTP status (400 bad
  input, 401 unauthorized, 404 unknown plugin, 503 no cluster reachable).
- **Parity with the CLI.** Both front-ends call `pkg/engine`, so conversion,
  auto-install, prune and ownership labels behave identically.
- **TLS.** Terminate TLS in front of the API (a reverse proxy / ingress) for
  production; the server speaks plain HTTP.
- The OpenAPI spec is the source of truth — browse it at `/docs`.

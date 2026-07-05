# Orcinus — Private Image Registries

Pulling images from a private registry needs registry credentials. Orcinus does
this the Kubernetes-native way: a **pull secret** (`kubernetes.io/dockerconfigjson`)
that workloads reference as an **imagePullSecret**. Two steps:

1. **Log in** — create the pull secret once (`orcinus secret create-registry`).
2. **Attach** it — per service with `x-orcinus-image-pull-secret`, or cluster-wide
   on the default service account.

---

## 1. Create the pull secret (log in)

```bash
orcinus secret create-registry regcred \
  --server registry.example.com \
  --username "$REG_USER" \
  --password "$REG_TOKEN"
# → registry secret "regcred" created — use it with `x-orcinus-image-pull-secret: regcred`
```

| Flag | Description |
|---|---|
| `--server` | Registry host (`registry.example.com`, `ghcr.io`, `docker.io`, …) |
| `--username`, `-u` | Registry username |
| `--password`, `-p` | Registry password or access token |
| `--email` | Optional email |
| `--namespace`, `-n` | Namespace to create the secret in (default `default`) |
| `--insecure` | Skip TLS verification for the login test (self-signed registries) |
| `--skip-login-check` | Create the secret without testing the login first |

**It tests the login first.** Before storing anything, orcinus authenticates to
the registry (Docker Registry v2 handshake — basic or bearer token). If the
credentials are wrong you get an error and **no secret is created**:

```
$ orcinus secret create-registry regcred --server registry.example.com -u me -p wrong
testing login to registry.example.com ...
error: login to https://registry.example.com failed: invalid credentials
```

It writes the same `.dockerconfigjson` format `docker login` produces. Re-running
updates it (idempotent). Create it in the **same namespace** as the workloads that
use it.

Common registries:

```bash
# Docker Hub (private repos) — use an access token as the password
orcinus secret create-registry dockerhub --server docker.io -u myuser -p "$DOCKERHUB_TOKEN"

# GitHub Container Registry (ghcr.io) — password is a PAT with read:packages
orcinus secret create-registry ghcr --server ghcr.io -u my-gh-user -p "$GHCR_PAT"

# GitLab registry
orcinus secret create-registry gitlab --server registry.gitlab.com -u my-user -p "$GITLAB_TOKEN"
```

---

## 2a. Attach per service (recommended)

Reference the secret from the compose service with `x-orcinus-image-pull-secret`
(a name or a list). Orcinus adds it to the workload's pod template:

```yaml
services:
  app:
    image: registry.example.com/team/app:1.0
    ports: ["8080"]
    x-orcinus-image-pull-secret: regcred      # or a list: [regcred, ghcr]
```

```bash
orcinus deploy -f orcinus.yml --wait
```

The generated Deployment/StatefulSet/DaemonSet (and Rollout) gets:

```yaml
spec:
  template:
    spec:
      imagePullSecrets:
        - name: regcred
```

You can also keep the secret in the same file as a raw manifest, but
`orcinus secret create-registry` avoids putting credentials in the compose file.

---

## 2b. Attach cluster-wide (all workloads in a namespace)

To pull from a private registry **without** annotating every service, add the
secret to the namespace's `default` service account — every pod then uses it:

```bash
orcinus secret create-registry regcred --server registry.example.com -u user -p "$TOKEN"

orcinus kubectl patch serviceaccount default \
  -p '{"imagePullSecrets":[{"name":"regcred"}]}'
```

(Repeat per namespace; `-n <ns>` on both commands for a non-default namespace.)

---

## Via the HTTP API

```bash
curl -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"regcred","server":"registry.example.com","username":"user","password":"tok"}' \
  http://localhost:8080/api/v1/secrets/docker-registry
```

Then deploy with `x-orcinus-image-pull-secret` as usual (see [API.md](./API.md)).

---

## Notes

- **Token over password.** Prefer a registry access token / PAT scoped to
  read/pull, not your account password.
- **Namespace-scoped.** A pull secret only works for workloads in its own
  namespace — create it wherever you deploy.
- **The orcinus cluster registry plugin** (`orcinus plugin install registry`) is a
  *different* thing — it runs an in-cluster registry to push to; this page is about
  pulling from an *external* private registry.
- See [`examples/private-registry/`](../examples/private-registry/orcinus.yml).

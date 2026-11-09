# Signed app: only signed images run, locked down

A small Go web service, built on [`orcinus/base`](../../images/base) (no
shell, non-root), signed with `orcinus sign`, and deployed to a cluster that
refuses anything unsigned. The full guide is [docs/SIGNING.md](../../docs/SIGNING.md).

| File | What |
|---|---|
| `main.go`, `go.mod` | the app (listens on :8080) |
| `Containerfile` | static build copied onto `orcinus/base` |
| `orcinus.yml` | the service: ingress, non-root, read-only, no capabilities, no privilege escalation |
| `ci/github-release.yml` | a pipeline that builds, pushes and signs on every tag |

Replace `registry.example.com` with your registry (log in first with
`docker login`), and `signed-app.example.com` with a host pointing at your
cluster.

## 1. Build and push

```bash
make -C ../.. image-base                    # orcinus/base:9 (or use your registry's copy)
docker build -t registry.example.com/signed-app:1.0 -f Containerfile .
docker push registry.example.com/signed-app:1.0
```

## 2. Sign

```console
$ orcinus sign keygen
$ orcinus sign image registry.example.com/signed-app:1.0
signed registry.example.com/signed-app@sha256:a3ecda765468b158e02d86b754e21427463658fd9a874da57a7ce7d138e6ff85
```

## 3. Make the cluster accept only your signatures

```bash
orcinus sign trust add default
orcinus sign enforce --mode audit      # first: see what else runs unsigned
orcinus sign status                    # sign or exempt what it lists
orcinus sign enforce --mode enforce
```

## 4. Deploy

```console
$ orcinus deploy -f orcinus.yml --project signed-app
applied 3 object(s) as project "signed-app"

$ curl -H "Host: signed-app.example.com" http://<cluster>/
hello from a signed image (pod web-89b68dc99-xwr5t, uid 65532)

$ orcinus kubectl get deploy web -o jsonpath='{.spec.template.spec.containers[0].image}'
registry.example.com/signed-app@sha256:a3ecda765468b158e02d86b754e21427463658fd9a874da57a7ce7d138e6ff85
```

The deployment runs the exact digest that was verified, not the `1.0` tag,
and the app runs as UID 65532.

## 5. An unsigned version is refused

Build and push `1.1` without signing it, point `orcinus.yml` at it, and deploy:

```console
$ orcinus deploy -f orcinus.yml --project signed-app
Error: image signing is enforced and these images are not signed by a trusted key:
  Deployment/web: registry.example.com/signed-app:1.1: not signed by a trusted key
sign them with `orcinus sign image <image>` (or exempt them: orcinus sign exempt add <image>)
```

Nothing was applied, and `1.0` keeps serving. `kubectl apply` of the same
image is refused by the cluster as well. After
`orcinus sign image registry.example.com/signed-app:1.1`, the deploy goes
through.

## 6. In CI

Give the pipeline its own key, and trust it in the cluster:

```bash
orcinus sign keygen ci
orcinus sign trust add ci
```

Copy `ci/github-release.yml` to `.github/workflows/`, and set the repository
secrets `ORCINUS_SIGNING_KEY` (the contents of `~/.orcinus/keys/ci.key`) and
`ORCINUS_SIGNING_PASSWORD`. Every `v*` tag then builds, pushes and signs the
image.

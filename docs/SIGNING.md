# Signed & immutable images

Make the cluster run **only images you signed**, **exactly as you signed
them**. Everything is done with the `orcinus` CLI. No other tool is needed.

```bash
orcinus sign keygen                          # 1. create a key pair
orcinus sign image registry.example.com/app:1.0   # 2. sign an image in its registry
orcinus sign trust add default               # 3. the cluster trusts your public key
orcinus sign enforce --mode audit            # 4. report unsigned images…
orcinus sign status                          #    …see what still needs signing
orcinus sign enforce --mode enforce          # 5. refuse unsigned images
```

- [How it works](#how-it-works)
- [Quick start](#quick-start)
- [Keys](#keys)
- [Signing images](#signing-images)
- [Turning enforcement on](#turning-enforcement-on)
- [Immutability: digests and locked-down containers](#immutability-digests-and-locked-down-containers)
- [Exemptions](#exemptions)
- [Deploying with signing on](#deploying-with-signing-on)
- [CI: signing in a pipeline](#ci-signing-in-a-pipeline)
- [Private and local registries](#private-and-local-registries)
- [Teams and key rotation](#teams-and-key-rotation)
- [Operations](#operations)
- [Troubleshooting](#troubleshooting)
- [What it does not protect against](#what-it-does-not-protect-against)
- [Command reference](#command-reference)

A complete walk-through with an app, a hardened compose file and a CI pipeline
is in [`examples/signed-app`](../examples/signed-app).

---

## How it works

```
 you / CI                          registry                        orcinus cluster
 ─────────                         ────────                        ───────────────
 orcinus sign image app:1.0  ──►   app@sha256:4f1c…                Kyverno admission policy
   (private key)                   app:sha256-4f1c….sig  ◄──────   checks every new pod:
                                   (the signature)                   signed by a trusted key?
                                                                     ✓ run it, pinned to @sha256:4f1c…
                                                                     ✗ refuse it
```

- **A signature covers a digest, not a tag.** `sign image app:1.0` looks up
  the digest that `app:1.0` points to now and signs that digest. If someone later
  pushes different content under `app:1.0`, that content has a new digest, and
  it is unsigned.
- **Signatures live next to the image.** Each one is stored in the same
  registry, under the tag `sha256-<digest>.sig`. It uses the cosign key-based
  format. You don't need extra infrastructure, and nothing goes to a public
  transparency log.
- **The cluster checks every pod.** [Kyverno](https://kyverno.io), installed for
  you on first use, runs as an admission webhook. Every new pod is checked,
  however it was created: `orcinus deploy`, `kubectl apply`, a Helm chart, an
  operator. A Deployment with an unsigned image is refused when it is created,
  not later when its pods fail.
- **What runs is pinned to the verified digest.** The pod is rewritten from
  `app:1.0` to `app@sha256:4f1c…`, so it runs exactly the content that was
  checked.
- **The settings live in the cluster** (ConfigMap `kyverno/orcinus-signing`), so
  everyone using the same kubeconfig sees and changes the same trusted keys and
  mode.

## Quick start

Signing an image needs push access to its registry (`docker login` first).

```console
$ orcinus sign keygen
Password for the new key (empty for none):
Repeat password:
private key: /home/me/.orcinus/keys/default.key
public key:  /home/me/.orcinus/keys/default.pub (SHA256:b855dc0ea85ff021)

$ orcinus sign image registry.example.com/app:1.0
Password for the signing key:
signed registry.example.com/app@sha256:1ccc301f7ea5dbf49281528607bc5c1fef273d3b00ecb4a515b54cbd83b518e1

$ orcinus sign verify --key default registry.example.com/app:1.0
✓ registry.example.com/app@sha256:1ccc301f… signed by default

$ orcinus sign trust add default
trusted default (SHA256:b855dc0ea85ff021)
image signing: off, 1 trusted key(s)

$ orcinus sign enforce --mode audit
installing kyverno (the admission policy engine)...
image signing: audit, 1 trusted key(s)

$ orcinus sign status
mode:      audit
admission: active
keys:      default
exempt:    namespaces: kube-system, kube-public, kube-node-lease, kyverno + plugin namespaces
           images: none
unsigned:  2 image(s) in running pods
  nginxdemos/hello:latest                  default/web-6c568d7fd7-np2jl (not signed by a trusted key)
  redis:7                                  default/cache-7d9c8b6f4-x2k8p (not signed by a trusted key)
```

Sign or [exempt](#exemptions) everything `status` lists, then turn it on:

```console
$ orcinus sign enforce --mode enforce
image signing: enforce, 1 trusted key(s)

$ orcinus kubectl run test --image=alpine:3.20
Error from server: admission webhook "ivpol.validate.kyverno.svc-fail-finegrained-orcinus-image-signing"
denied the request: Policy orcinus-image-signing failed: image not signed by a key this cluster trusts
(orcinus sign image <image>; orcinus sign status)
```

## Keys

```bash
orcinus sign keygen            # ~/.orcinus/keys/default.key + default.pub
orcinus sign keygen ci         # a second key, e.g. for the CI pipeline
orcinus sign keygen --force    # replace a key (images it signed stay signed by the old one)
```

- **Algorithm:** ECDSA P-256.
- **Private key** (`.key`): PKCS#8, encrypted with your password (scrypt +
  NaCl secretbox), file mode `0600`. Never commit it, and never give it to the
  cluster.
- **Public key** (`.pub`): PEM. This is the part you share; the cluster stores
  it with `sign trust add`.
- **The password** is asked for on the terminal. Without a terminal, it is taken
  from `ORCINUS_SIGNING_PASSWORD`, which is how CI sets it.
- **Choosing a key:** `--key` takes a name (`ci` → `~/.orcinus/keys/ci.key`)
  or a path (`./keys/ci.key`).

## Signing images

```bash
orcinus sign image registry.example.com/app:1.0
orcinus sign image registry.example.com/app:1.0 registry.example.com/worker:1.0   # several
orcinus sign image --key ci registry.example.com/app:1.0
orcinus sign image registry.example.com/app@sha256:4f1c…                          # by digest
```

- **Re-signing is a no-op.** Signing an image again with the same key changes
  nothing.
- **Several keys can sign the same image.** Each signature is stored, and any
  one trusted key is enough.
- **Multi-arch images:** the signature covers the image index, which is what
  the tag points to, so every architecture under it counts as signed.
- **Signing means vouching for the content.** Sign images you built or
  reviewed, typically in the pipeline right after `docker push`. Signing a
  public image (`redis:7`) means "I accept this exact digest", which is a
  legitimate use: the signature is pushed to *your* copy of it, so sign the copy
  you mirror into your own registry.

Check a signature yourself:

```bash
orcinus sign verify registry.example.com/app:1.0              # against the cluster's trusted keys
orcinus sign verify --key default registry.example.com/app:1.0 # against a local .pub
```

## Turning enforcement on

| Mode | New pods with an unsigned image | Use it to |
|---|---|---|
| `off` | run | turn checking off (the default; the policy is removed, settings are kept) |
| `audit` | run; listed by `sign status`, warned by `deploy` | find out what still needs signing |
| `enforce` | **refused** | only signed images run |

```bash
orcinus sign enforce --mode audit
orcinus sign enforce --mode enforce
orcinus sign enforce --mode off
```

The first `enforce` (in either mode) installs the `kyverno` plugin, which takes
about a minute. It is a normal plugin, visible in `orcinus plugin list`.

**Recommended rollout:**
1. `trust add` your key(s) and set `--mode audit`.
2. Sign your images, and exempt the ones you don't control.
3. `sign status` until it lists nothing you care about.
4. `--mode enforce`.

**Pods that are already running are not stopped** when you switch to enforce.
The check happens when a pod is created. But an unsigned pod that restarts
later (node reboot, rollout, eviction) is refused, so step 3 matters.

## Immutability: digests and locked-down containers

**Content can't be swapped.** With signing on, every image is pinned to the
digest that was verified, in two places:

- **`orcinus deploy`:** the image is rewritten in the Deployment itself
  (`image: registry.example.com/app@sha256:4f1c…`). The cluster's spec is then
  exactly what was checked, and `orcinus rollback` returns to exact digests.
- **Any other path** (kubectl, Helm, operators): the admission policy rewrites
  each pod to its digest.

Tested on a live cluster: after pushing other content under an already-signed
tag, new pods with that tag were refused, while pods already running kept
their pinned digest.

**Containers can't be changed while running.** Signing proves *what* starts.
To keep it from changing *after* it starts, lock the container down in the
compose file:

```yaml
services:
  app:
    image: registry.example.com/app:1.0
    user: "65532:65532"            # not root
    read_only: true                # read-only root filesystem
    cap_drop: [ALL]                # no Linux capabilities
    security_opt: [no-new-privileges:true]   # no setuid escalation
    tmpfs: [/tmp]                  # scratch space, in memory
```

| compose | Kubernetes `securityContext` |
|---|---|
| `user: "65532:65532"` | `runAsUser: 65532`, `runAsGroup: 65532` |
| `read_only: true` | `readOnlyRootFilesystem: true` |
| `cap_drop: [ALL]` | `capabilities.drop: [ALL]` |
| `security_opt: [no-new-privileges:true]` | `allowPrivilegeEscalation: false` |
| `tmpfs: [/tmp]` | memory-backed `emptyDir` on `/tmp` |

Build on [`orcinus/base`](../images/base) for an image with no shell, no
package manager and no programs other than yours. Even someone who gets in can
then run nothing new.

## Exemptions

Some images can't be signed by you, and they don't need to be:

| Always exempt | Why |
|---|---|
| namespaces `kube-system`, `kube-public`, `kube-node-lease` | the cluster's own components (DNS, ingress, metrics, …) |
| namespace `kyverno` | the policy engine itself |
| every namespace a plugin installed into | upstream images of the add-ons (cert-manager, monitoring, …), including the pods their operators create later. They carry the `orcinus.io/plugin` label, set by `orcinus plugin install`. |
| exact images a plugin runs in `default` | e.g. the monitoring plugin's prometheus-operator. They are exempt by their exact reference, not the whole namespace. |

Add your own:

```bash
orcinus sign exempt add sandbox                        # a namespace
orcinus sign exempt add docker.io/library/busybox      # an image prefix (any tag/digest under it)
orcinus sign exempt add registry.example.com/thirdparty/
orcinus sign exempt rm sandbox
```

An argument containing `/` or `:` is an image prefix; anything else is a
namespace. Prefer exempting the narrowest prefix you can: an exempted image
runs unchecked everywhere.

## Deploying with signing on

`orcinus deploy` (and the HTTP API and MCP `deploy` tools, which share the same
engine) check the images *before* applying anything:

```console
$ orcinus deploy -f orcinus.yml
Error: image signing is enforced and these images are not signed by a trusted key:
  Deployment/worker: registry.example.com/worker:2.3: not signed by a trusted key
sign them with `orcinus sign image <image>` (or exempt them: orcinus sign exempt add <image>)
```

- **Nothing is half-applied.** The check runs before any object is sent, so a
  project never ends up partly deployed.
- **In audit mode** the deploy goes through, with a warning per unsigned
  image:

  ```
  warning: unsigned image (signing is in audit mode): Deployment/worker: registry.example.com/worker:2.3: not signed by a trusted key
  ```

- **Signed images are pinned** to their digest in the applied Deployments (see
  [Immutability](#immutability-digests-and-locked-down-containers)).

## CI: signing in a pipeline

Give the pipeline its own key, so it can be revoked without touching yours:

```bash
orcinus sign keygen ci          # set a password
orcinus sign trust add ci       # the cluster accepts the pipeline's signatures
```

Store `~/.orcinus/keys/ci.key` and its password as CI secrets. When `--key` is
not given, `sign image` reads the key PEM from `ORCINUS_SIGNING_KEY` and the
password from `ORCINUS_SIGNING_PASSWORD`. Signing needs no cluster access.

GitHub Actions:

```yaml
# .github/workflows/release.yml
jobs:
  image:
    runs-on: ubuntu-latest
    permissions: { contents: read, packages: write }
    steps:
      - uses: actions/checkout@v4
      - uses: docker/login-action@v3
        with: { registry: ghcr.io, username: "${{ github.actor }}", password: "${{ secrets.GITHUB_TOKEN }}" }
      - run: |
          IMAGE=ghcr.io/${{ github.repository }}:${{ github.sha }}
          docker build -t "$IMAGE" . && docker push "$IMAGE"
          curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install.sh | sh
          orcinus sign image "$IMAGE"
        env:
          ORCINUS_SIGNING_KEY: ${{ secrets.ORCINUS_SIGNING_KEY }}
          ORCINUS_SIGNING_PASSWORD: ${{ secrets.ORCINUS_SIGNING_PASSWORD }}
```

GitLab CI:

```yaml
sign:
  stage: release
  script:
    - docker login -u "$CI_REGISTRY_USER" -p "$CI_REGISTRY_PASSWORD" "$CI_REGISTRY"
    - docker build -t "$CI_REGISTRY_IMAGE:$CI_COMMIT_SHA" . && docker push "$CI_REGISTRY_IMAGE:$CI_COMMIT_SHA"
    - curl -fsSL https://raw.githubusercontent.com/orcinustools/orcinus/main/install.sh | sh
    - orcinus sign image "$CI_REGISTRY_IMAGE:$CI_COMMIT_SHA"
  variables:
    ORCINUS_SIGNING_KEY: $ORCINUS_SIGNING_KEY            # a CI/CD variable holding ci.key
    ORCINUS_SIGNING_PASSWORD: $ORCINUS_SIGNING_PASSWORD
```

Deploy the image by the same reference you signed (the tag, or better the
digest that `sign image` printed).

## Private and local registries

- **Signing and verifying from the CLI** use your docker credentials
  (`~/.docker/config.json`).
- **The cluster** reads signatures through Kyverno, which needs its own pull
  secret in the `kyverno` namespace:

  ```bash
  orcinus secret create-registry regcred -n kyverno --server registry.example.com -u USER -p TOKEN
  orcinus sign enforce --mode enforce --pull-secret regcred
  ```

  The workloads themselves still need their usual pull secret
  ([REGISTRY.md](./REGISTRY.md)).

**Plain-HTTP registries** (a local lab registry): add `--insecure-registry` to
`sign image` and `sign verify`, and to `sign enforce` so the cluster may read
signatures over HTTP too:

```bash
orcinus sign image --insecure-registry 10.0.0.5:5000/app:1.0
orcinus sign enforce --mode enforce --insecure-registry
```

## Teams and key rotation

- **Several trusted keys:** one per person or per pipeline. A pod is admitted
  if *any* trusted key signed each of its images.

  ```bash
  orcinus sign trust add alice ./alice.pub
  orcinus sign trust add ci ./ci.pub
  orcinus sign trust ls
  ```

- **Revoking** a key (someone leaves, a key leaks): `orcinus sign trust rm alice`.
  New pods signed only by that key are refused; running ones keep running until
  they restart. Re-sign what you still want with another key *before* removing
  it, and run `sign status` to find what that would affect.
- **Rotating:** `keygen new` → `trust add new` → re-sign the images that are
  still deployed → `trust rm old`.
- **The last key can't be removed** while signing is on, because with no keys
  there would be no policy and everything would run. Turn signing off first if
  that is what you mean.

## Operations

```bash
orcinus sign status                 # mode, keys, exemptions, unsigned running images
orcinus plugin list                 # kyverno shows as installed
orcinus kubectl get imagevalidatingpolicy orcinus-image-signing
```

`status` also shows whether the check is really in force:

```
admission: active
admission: INACTIVE: Kyverno admission controller is not running — pods are NOT being checked
```

- **Kyverno must be running.** Its admission controller is what refuses pods.
  Kyverno removes its webhooks when it is shut down, so scaling it to zero or
  uninstalling it turns the check off. Keep the `kyverno` namespace writable by
  cluster admins only, and watch `admission:` in `sign status`.
- **Break-glass.** If you need unsigned pods to run right now:
  `orcinus sign enforce --mode off`. Trusted keys and exemptions are kept, and
  `--mode enforce` restores everything.
- **Turning it off for good:** `orcinus sign enforce --mode off`, then
  optionally `orcinus plugin remove kyverno`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `not signed by a trusted key (no signatures)` | the digest the tag points to *now* was never signed. It was rebuilt or retagged after signing: sign it again. |
| `not signed by a trusted key` (signatures exist) | signed by a key the cluster doesn't trust: `sign trust ls`, then `sign verify --key <name>` |
| `wrong password for the signing key` | wrong `ORCINUS_SIGNING_PASSWORD` or terminal input |
| `push signature: … UNAUTHORIZED` | `docker login` to the registry. Signing pushes a tag. |
| deploy is refused but `sign verify` passes locally | the cluster can't read the registry: `--pull-secret` (private) or `--insecure-registry` (HTTP) on `sign enforce` |
| a plugin's pods are refused | the plugin was installed by an older orcinus, so its namespace has no `orcinus.io/plugin` label. Run `orcinus sign enforce --mode <current>` again: it labels the namespaces of installed plugins. |
| `admission: INACTIVE` | `orcinus kubectl -n kyverno get pods`. The admission controller must be Running. |
| everything in a namespace must run unsigned | `orcinus sign exempt add <namespace>` |

## What it does not protect against

- **A stolen private key.** Anything signed with it is trusted. Protect keys
  with a password, keep one key per person/pipeline, and revoke with
  `trust rm`.
- **Bad code you signed.** A signature says *who* vouched for an image, not
  that it is safe. Sign what CI built from reviewed code, and scan images (e.g.
  `trivy image`).
- **Cluster admins.** Anyone who can edit the `kyverno` namespace, the
  ImageValidatingPolicy, or namespace labels (`orcinus.io/plugin`) can turn the
  check off. Restrict those rights.
- **Exempted namespaces and images**, which run unchecked by design.
- **Workloads running before enforcement** keep running until they restart.
- **No transparency log.** Signatures stay private, but there is no public
  record of when something was signed.

## Command reference

| Command | Description |
|---|---|
| `orcinus sign keygen [name] [--force]` | Create `~/.orcinus/keys/<name>.key/.pub` (name: `default`) |
| `orcinus sign image <image>... [--key k] [--insecure-registry]` | Sign images in their registry |
| `orcinus sign verify <image>... [--key k]... [--insecure-registry]` | Check signatures (default: against the cluster's trusted keys) |
| `orcinus sign trust add <name> [key.pub]` | Trust a public key |
| `orcinus sign trust rm <name>` | Stop trusting a key |
| `orcinus sign trust ls` | List trusted keys with fingerprints |
| `orcinus sign enforce --mode audit\|enforce\|off [--pull-secret s]... [--insecure-registry]` | Set the mode (installs Kyverno on first use) |
| `orcinus sign exempt add\|rm <namespace\|image-prefix>...` | Manage exemptions |
| `orcinus sign status` | Mode, admission health, keys, exemptions, unsigned running images |

Cluster commands take `--kubeconfig`. Environment variables:
`ORCINUS_SIGNING_KEY` (private key PEM, used when `--key` is not given) and
`ORCINUS_SIGNING_PASSWORD`.

# Orcinus — Plugins & Cluster Add-ons

Plugins let you add cluster capabilities — TLS certificates, extra ingress
controllers, metrics — by **picking options**, not by hand-applying manifests.

> **Status:** `orcinus plugin list|info|install|remove`, the ingress/TLS
> `x-orcinus-*` sugar, and auto-installing cert-manager on `deploy` are all
> **implemented**. More catalog entries and profiles are planned.

---

## Table of Contents

- [What ships in a cluster by default](#what-ships-in-a-cluster-by-default)
- [The `orcinus plugin` command](#the-orcinus-plugin-command)
- [Plugin catalog](#plugin-catalog)
- [Exposing an app with HTTPS (end to end)](#exposing-an-app-with-https-end-to-end)
- [Ingress & TLS `x-orcinus-*` keys](#ingress--tls-x-orcinus--keys)
- [How plugins work under the hood](#how-plugins-work-under-the-hood)
- [Roadmap](#roadmap)

---

## What ships in a cluster by default

An orcinus cluster (`orcinus cluster init`) already comes with:

- **Ingress:** Traefik, the default ingress controller (class `traefik`), so
  `x-orcinus-expose: ingress` works with no extra install.
- **Storage:** a `local-path` default StorageClass, so PVCs bind out of the box.

To serve web traffic from outside the host, publish the ingress ports when you
create the cluster:

```bash
orcinus cluster init --http-port 80 --https-port 443
```

Plugins are for everything beyond these defaults (TLS automation, nginx, metrics).

---

## The `orcinus plugin` command

```
orcinus plugin list                       # catalog + install status
orcinus plugin info <name>                # details for one plugin
orcinus plugin install <name> [options]
orcinus plugin remove <name>              # delete what it installed
```

```bash
orcinus plugin install cert-manager --email me@example.com
orcinus plugin install cert-manager --email me@example.com --staging   # LE staging
orcinus plugin install ingress-nginx
orcinus plugin install metrics-server
```

Installed plugins are recorded in `~/.orcinus/plugins.json`.

---

## Plugin catalog

| Plugin | Options | Installs |
|---|---|---|
| `cert-manager` | `--email` (required), `--staging` | cert-manager + a `letsencrypt` `ClusterIssuer` (prod, or staging) |
| `ingress-nginx` | — | NGINX ingress controller (class `nginx`) |
| `metrics-server` | — | metrics-server (`kubectl top`, HPA) |
| `monitoring` | — | Prometheus Operator (CRDs + operator) |
| `argo-rollouts` | — | Argo Rollouts controller for canary/blue-green (`x-orcinus-rollout`) |
| `dashboard` | — | Kubernetes Dashboard (web UI) |
| `registry` | — | In-cluster image registry (`registry.orcinus-registry.svc:5000`) |
| `grafana` | — | Grafana (point at Prometheus) |
| `storage` | `--provider`, `--size`, `--replicas`, `--nfs-server`, `--nfs-path`, `--ceph-*` | Storage backends — see below |

All plugin versions are **pinned** (see `orcinus plugin info <name>`), so installs
are reproducible. `cert-manager` waits for its webhook before creating the issuer.

### Profiles

Install a common set at once with `--profile`:

```bash
orcinus plugin install --profile web --email me@example.com   # cert-manager + ingress-nginx
orcinus plugin install --profile observability                # metrics-server + monitoring + grafana
```

| Profile | Plugins |
|---|---|
| `web` | `cert-manager`, `ingress-nginx` |
| `observability` | `metrics-server`, `monitoring`, `grafana` |

### Storage providers

`storage` is a family selected with `--provider`:

| Provider | Kind | Command |
|---|---|---|
| `local-path` | block/file (default) | already in the cluster — nothing to install |
| `longhorn` | distributed block (HA) | `orcinus plugin install storage --provider longhorn [--replicas 3]` |
| `nfs` | shared file (RWX) | `orcinus plugin install storage --provider nfs --nfs-server 10.0.0.9 --nfs-path /export` |
| `minio` | object (S3-compatible) | `orcinus plugin install storage --provider minio [--replicas 4] [--size 20Gi]` |
| `rook-ceph` | block+file+object (HA) | `orcinus plugin install storage --provider rook-ceph [--ceph-device-filter '^sd[b-d]'] [--ceph-failure-domain host] [--replicas 3]` |

- **nfs** deploys the nfs-subdir-external-provisioner + a `nfs` StorageClass
  backed by your existing NFS server (supports `ReadWriteMany`).
- **minio** deploys MinIO (object storage) exposing the S3 API on `minio:9000`
  and a console on `:9001` in namespace `orcinus-storage` (default creds
  `minioadmin` / `minioadmin` — change them). Add `--replicas N` (≥2) for
  **distributed/HA** mode (StatefulSet, erasure-coded; ≥4 recommended). Pods are
  auto-spread across nodes (anti-affinity + topology spread).
- **longhorn** needs `open-iscsi` on every node; replicates volumes across nodes.
  `--replicas N` adds a `longhorn-ha` StorageClass with that replica count.
- **rook-ceph** installs the Rook operator + a `CephCluster`, a replicated
  `CephBlockPool`, and a `ceph-block` StorageClass. Tune with `--ceph-device-filter`
  (which disks), `--ceph-failure-domain` (`host`/`osd`/`rack`), and `--replicas`
  (pool replica size). Needs multiple nodes with raw disks.

Remove with the same provider, e.g. `orcinus plugin remove storage --provider minio`.

For fault-tolerant setups (replicas across nodes) see
[`HA-STORAGE.md`](./HA-STORAGE.md).

### Auto-install on deploy

If a service uses `x-orcinus-tls` and cert-manager isn't installed yet, pass
`--acme-email` to `orcinus deploy` and orcinus installs cert-manager for you
before applying:

```bash
orcinus deploy --wait --acme-email you@example.com
```

Without cert-manager and without `--acme-email`, deploy stops with a clear
message telling you to install the plugin.

---

## Exposing an app with HTTPS (end to end)

This is the full, verified flow — a web app on a public domain with a **trusted
Let's Encrypt certificate**:

```bash
# 1. Cluster with ingress ports published (DNS: app.example.com → this host)
orcinus cluster init --http-port 80 --https-port 443

# 2. TLS automation
orcinus plugin install cert-manager --email you@example.com

# 3. Deploy — HTTPS from three x-orcinus-* lines
cat > orcinus.yml <<'EOF'
services:
  web:
    image: traefik/whoami:v1.10
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: app.example.com
    x-orcinus-tls: letsencrypt
EOF
orcinus deploy --wait
```

cert-manager solves the ACME HTTP-01 challenge through Traefik and issues the
cert into `web-tls`; Traefik then serves `https://app.example.com` with a trusted
certificate. Requirements: public DNS for the host and inbound 80/443.

> Verified against a live domain: issuer `O = Let's Encrypt`, subject
> `CN = <your host>`, served over HTTPS by Traefik.
>
> This flow has a **committed e2e test** (`TestLiveIngressTLS`, uses LE staging so
> it's repeatable). Run it against your own domain:
> `make e2e-tls ORCINUS_E2E_DOMAIN=<host> ORCINUS_E2E_DOCKER="sudo docker"`.

---

## Ingress & TLS `x-orcinus-*` keys

| Key | Values | Effect |
|---|---|---|
| `x-orcinus-expose` | `ingress` \| `nodeport` \| `loadbalancer` \| `clusterip` | How the service is exposed |
| `x-orcinus-host` | hostname | Ingress host |
| `x-orcinus-tls` | issuer name (e.g. `letsencrypt`) | TLS block + `cert-manager.io/cluster-issuer` annotation |
| `x-orcinus-path` | path (default `/`) | Ingress path |
| `x-orcinus-port` | port number | Which service port the ingress routes to |
| `x-orcinus-ingress-class` | `traefik` \| `nginx` \| … | Ingress class |

Ingress is controller-agnostic: the same object works whether the class is
`traefik` or `nginx`, and cert-manager TLS works with either. Traefik is the
default because it ships with the cluster.

Design decisions:
- **TLS is opt-in** — plain HTTP unless `x-orcinus-tls` is set.
- **cert-manager is the TLS path** (portable, standard).
- The **issuer `letsencrypt` is created by the `cert-manager` plugin** (needs
  `--email`); or point `x-orcinus-tls` at an issuer you manage.

---

## How plugins work under the hood

A plugin is a registry entry mapping a name to (1) manifest URLs and (2) optional
post-install objects built from your options (e.g. a `ClusterIssuer`). `install`
reuses the `orcinus deploy` engine: it fetches + server-side-applies the
manifests, waits for the named Deployments, applies post-install objects (with a
fresh API discovery so newly-installed CRDs are visible), and records state in
`~/.orcinus/plugins.json`. Because [`orcinus deploy -f <url>`](./USAGE.md#55-orcinus-deploy)
already works, the registry points at upstream release URLs directly.

---

## Roadmap

- Custom profiles defined by the user (not just the built-in `web` /
  `observability`).
- Upgrade flow (`plugin upgrade`) to move a plugin to a newer pinned version.
- Health/status in `plugin list` (installed **and** ready).

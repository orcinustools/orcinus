# Orcinus — Plugins & Cluster Add-ons

Plugins let you add cluster capabilities — TLS certificates, extra ingress
controllers, metrics — by **picking options**, not by hand-applying manifests.

> **Status:** `orcinus plugin list|install` and the ingress/TLS `x-orcinus-*`
> sugar are **implemented**. `remove`/`info` and more catalog entries are planned.

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
orcinus plugin install <name> [options]
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

`cert-manager` waits for its webhook to be ready before creating the issuer.

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

- `orcinus plugin remove` / `info`, and pinned plugin versions.
- More catalog entries: storage drivers (Longhorn), monitoring (Prometheus +
  Grafana), a local registry, a dashboard.
- Profiles: install a set at once (e.g. `--profile web` = ingress + cert-manager).
- Auto-install a plugin when an `x-orcinus-*` key needs it (e.g. `x-orcinus-tls`
  pulling in cert-manager).

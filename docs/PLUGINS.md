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
| `cert-manager` | `--email` (required), `--staging`, `--dns cloudflare --dns-token <t>` | cert-manager + a `letsencrypt` `ClusterIssuer` (HTTP-01); with `--dns`, also a `letsencrypt-dns` issuer (DNS-01, for wildcards) |
| `ingress-nginx` | — | NGINX ingress controller (class `nginx`) |
| `metrics-server` | — | metrics-server (`kubectl top`, HPA) |
| `monitoring` | — | Prometheus Operator (CRDs + operator) |
| `argo-rollouts` | — | Argo Rollouts controller for canary/blue-green (`x-orcinus-rollout`) |
| `dashboard` | — | Kubernetes Dashboard (web UI) |
| `registry` | — | In-cluster image registry (`registry.orcinus-registry.svc:5000`) |
| `grafana` | — | Grafana (point at Prometheus) |
| `kubevirt` | `--emulation`, `--cdi` | KubeVirt (run VMs on the cluster) — see below |
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

### Virtual machines (KubeVirt)

`kubevirt` lets the cluster schedule **VMs next to containers**. It installs the
KubeVirt operator (namespace `kubevirt`), waits for `virt-operator`, then applies
a `KubeVirt` custom resource — which is what brings up `virt-api`,
`virt-controller`, and `virt-handler`:

```bash
orcinus plugin install kubevirt               # nodes must have /dev/kvm
orcinus plugin install kubevirt --emulation   # no /dev/kvm: QEMU software emulation (slower)
orcinus plugin install kubevirt --cdi         # + CDI, for disk images from URLs/registries
```

- **Hardware virtualization:** VMs want `/dev/kvm` on the node. A containerized
  orcinus cluster only has it if the host passes it through, so if VMs stay
  pending on `devices.kubevirt.io/kvm`, re-install with `--emulation`.
- **`--cdi`** installs the Containerized Data Importer (namespace `cdi`), which
  adds `DataVolume`s — import a cloud image (URL, registry, or upload) into a PVC
  and boot a VM off it.
- Check readiness with
  `orcinus kubectl -n kubevirt get kubevirt kubevirt -o jsonpath='{.status.phase}'`
  (`Deployed` when the control plane is up).

A VM is a plain manifest, so `orcinus deploy -f` applies it — and a compose
service and a VM can live in the same file:

```yaml
# vm.yml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: ubuntu
spec:
  runStrategy: Always                # Halted = defined but not started
  template:
    metadata:
      labels:
        app: ubuntu                  # copied to the VMI's pod → Service selector
    spec:
      domain:
        memory:
          guest: 1Gi
        devices:
          rng: {}                    # virtio-rng: cloud-init won't stall on entropy
          disks:
            - name: rootdisk
              disk: { bus: virtio }
          interfaces:
            - name: default
              masquerade: {}
      networks:
        - name: default
          pod: {}
      volumes:
        - name: rootdisk
          containerDisk:
            image: quay.io/containerdisks/ubuntu:24.04
```

```bash
orcinus deploy -f vm.yml
orcinus kubectl get vmi                       # the running VM instance
```

Point a normal `Service` at the pod labels above and containers reach the VM by
DNS name, like any other backend.

**Disk options.** A `containerDisk` boots the cloud image from a registry with a
throwaway overlay — nothing to provision, but **writes are lost on restart**. For a
persistent disk, install with `--cdi` and use a `DataVolume` (imported once into a
PVC).

**Distro images.** [`quay.io/containerdisks`](https://quay.io/organization/containerdisks)
publishes maintained multi-arch (amd64/arm64) cloud images — swap the one line:

| Image | Tags |
|---|---|
| `quay.io/containerdisks/ubuntu` | `22.04`, `24.04` |
| `quay.io/containerdisks/fedora` | `40` … `44` |
| `quay.io/containerdisks/debian` | `11`, `12`, `13` |
| `quay.io/containerdisks/centos-stream` | `9`, `10` |
| `quay.io/containerdisks/almalinux` | `9`, `10` |
| `quay.io/containerdisks/opensuse-leap` | `15.6`, `16.0` |

(also `opensuse-tumbleweed`, `opensuse-microos`, `centos`.) Give each guest a login
with a `cloudInitNoCloud` volume — an explicit `users:` block works the same on
every distro, so you don't need each image's default account.

**Reaching a VM from outside (SSH, public IP).** What you can publish depends on
the cluster runtime:

| `cluster init --runtime` | Host ports published | External SSH |
|---|---|---|
| `docker` (default) | API `6443` + `--http-port`/`--https-port` | **no** — a NodePort/LoadBalancer binds inside the cluster container; use `orcinus kubectl port-forward svc/<vm> 2222:22 --address 0.0.0.0` |
| `standalone` | none needed — k3s runs on the host, so the node IP *is* the host IP | **yes** — NodePort or `type: LoadBalancer` (k3s ServiceLB) lands on the public IP |

```bash
orcinus cluster init --runtime standalone --http-port 80 --https-port 443 --advertise <public-ip>
orcinus secret create vm-ssh --from-literal orcinus="$(cat ~/.ssh/id_ed25519.pub)"
```

Reference the Secret from the VM and KubeVirt injects the key — rotate by updating
the Secret, not the VM:

```yaml
      accessCredentials:
        - sshPublicKey:
            source:
              secret:
                secretName: vm-ssh
            propagationMethod:
              noCloud: {}                  # → the image's default user (ubuntu, fedora, …)
              # qemuGuestAgent:            # → named users; needs qemu-guest-agent in the guest
              #   users: [orcinus]
```

Then `ssh -p 2222 ubuntu@<public-ip>` through a `LoadBalancer` Service
(`port: 2222` → `targetPort: 22`; the host's own sshd usually owns 22). Set
`ssh_pwauth: false` in cloud-init so a public VM is key-only. HTTP works on either
runtime — put a `Service` + `Ingress` in front of the VM and Traefik serves it on
the published 80/443. Full file: [`examples/kubevirt/ssh-public.yml`](../examples/kubevirt/ssh-public.yml).

Console/VNC access needs upstream `virtctl` (`virtctl console ubuntu`), which is
also how you start/stop a `Halted` VM (`virtctl start ubuntu`); without it, patch
`spec.runStrategy`. `virtctl ssh ubuntu@<vm>` tunnels over the API server, so it
needs no published port at all.

Runnable: [`examples/kubevirt`](../examples/kubevirt/orcinus.yml) — a container +
Ubuntu/Fedora VMs on one network, a six-distro catalog
([`distros.yml`](../examples/kubevirt/distros.yml)), and a persistent-disk VM
serving HTTP through a Service
([`cdi-datavolume.yml`](../examples/kubevirt/cdi-datavolume.yml)).

`orcinus plugin remove kubevirt` deletes the `KubeVirt` CR first, then the
operator — give the CR's finalizer a moment before re-installing.

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
certificate. Requirements: public DNS for the host and inbound 80/443. Runnable
example: [`examples/ingress-tls`](../examples/ingress-tls/orcinus.yml).

> Verified against a live domain: issuer `O = Let's Encrypt`, subject
> `CN = <your host>`, served over HTTPS by Traefik.
>
> This flow has a **committed e2e test** (`TestLiveIngressTLS`, uses LE staging so
> it's repeatable). Run it against your own domain:
> `make e2e-tls ORCINUS_E2E_DOMAIN=<host> ORCINUS_E2E_DOCKER="sudo docker"`.

---

## Wildcard domains & custom certificates

**Wildcard (`*.example.com`)** needs a **DNS-01** issuer (HTTP-01 can't do
wildcards). Install cert-manager with a DNS provider, which adds a
`letsencrypt-dns` `ClusterIssuer`:

```bash
orcinus plugin install cert-manager --email you@example.com --dns cloudflare --dns-token <api-token>
```
```yaml
services:
  app:
    image: myapp
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: "*.example.com"
    x-orcinus-tls: letsencrypt-dns     # the DNS-01 issuer
```

**Custom / bring-your-own cert** (incl. a wildcard cert you already have): put it
in a TLS Secret and reference it — no cert-manager, no ACME:

```bash
orcinus secret create-tls star-example --cert fullchain.pem --key privkey.pem
```
```yaml
services:
  app:
    image: myapp
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: "*.example.com"
    x-orcinus-tls-secret: star-example
```

`x-orcinus-tls-secret` takes precedence over `x-orcinus-tls`.

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

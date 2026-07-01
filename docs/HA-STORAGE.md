# Orcinus — High-Availability Storage

How to set up storage that survives a node failure. HA storage keeps **replicas
of your data on multiple nodes**, so a pod can reschedule elsewhere and still read
its volumes.

Prerequisite: a **multi-node cluster** — HA storage needs somewhere to put the
replicas. See [`CLUSTER.md`](./CLUSTER.md). On a single node you can *run* these
backends, but they are not fault-tolerant.

For the plugin commands, see [`PLUGINS.md`](./PLUGINS.md).

---

## Table of Contents

- [Which backend for what](#which-backend-for-what)
- [HA block storage — Longhorn](#ha-block-storage--longhorn)
- [HA object storage — MinIO distributed](#ha-object-storage--minio-distributed)
- [HA file storage — NFS notes](#ha-file-storage--nfs-notes)
- [Full distributed storage — Rook-Ceph](#full-distributed-storage--rook-ceph)
- [Verifying & caveats](#verifying--caveats)

---

## Which backend for what

| Need | Backend | HA model |
|---|---|---|
| Block volumes (PVC RWO) for databases | **Longhorn** | synchronous replicas across nodes |
| Object storage (S3) for apps/backups | **MinIO distributed** | erasure coding across ≥4 drives/nodes |
| Shared file (PVC RWX) | **NFS** | *only as HA as your NFS server* (see notes) |
| One system for block+file+object | **Rook-Ceph** (`--provider rook-ceph`) | Ceph replication/erasure (advanced) |

---

## HA block storage — Longhorn

Longhorn keeps synchronous replicas of each volume on multiple nodes (default 3).

```bash
# 3+ worker nodes recommended; every node needs open-iscsi installed
orcinus plugin install storage --provider longhorn
```

- Use the `longhorn` StorageClass for your PVCs; Longhorn replicates each volume.
- **Custom replica count:** pass `--replicas N` to create an extra StorageClass
  `longhorn-ha` with `numberOfReplicas=N`:
  ```bash
  orcinus plugin install storage --provider longhorn --replicas 3
  ```
  With 3 replicas a volume survives losing up to 2 nodes. Use `longhorn-ha` in
  your PVC's `storageClassName`.
- Requirement: `open-iscsi` on every node, and ≥ replica-count nodes.

Example PVC:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: db-data }
spec:
  storageClassName: longhorn
  accessModes: [ReadWriteOnce]
  resources: { requests: { storage: 10Gi } }
```

---

## HA object storage — MinIO distributed

MinIO in distributed mode erasure-codes objects across N drives, tolerating the
loss of up to half of them.

```bash
# distributed mode: >=4 recommended (erasure coding needs >=4 drives)
orcinus plugin install storage --provider minio --replicas 4 --size 50Gi
```

This creates a StatefulSet `minio` (one PVC per pod) plus a headless Service for
peer discovery and a `minio` Service for clients:

- S3 API: `minio.orcinus-storage.svc:9000`
- Console: `:9001`
- Default credentials `minioadmin` / `minioadmin` — **change them**.

Pods are **automatically spread across nodes** — orcinus adds pod anti-affinity
(`kubernetes.io/hostname`) plus a topology-spread constraint (both soft, so it
still schedules on a single node). On a multi-node cluster the replicas land on
different nodes for real fault tolerance.

> Verified: on a 2-node cluster, `--replicas 4` → StatefulSet 4/4 Ready with 2
> pods on each node; four bound PVCs; erasure set formed.

---

## HA file storage — NFS notes

`orcinus plugin install storage --provider nfs --nfs-server … --nfs-path …` gives
you `ReadWriteMany` volumes shared by many pods — but availability equals your NFS
server's. For HA file storage, either point it at an already-HA NAS/NFS cluster,
or use CephFS via Rook-Ceph below.

---

## Full distributed storage — Rook-Ceph

Rook runs Ceph on Kubernetes, providing HA **block, file, and object** from one
system. It is powerful but heavy: it wants raw block devices and several nodes.

It is a first-class provider — one command installs the operator (CRDs + common +
operator) and creates a `CephCluster` (uses all nodes/devices, 3 mons):

```bash
orcinus plugin install storage --provider rook-ceph
```

Then use the block/CephFS/object StorageClasses Ceph exposes. Requirements:
**multiple nodes with unformatted raw disks** and `dataDirHostPath` writable
(`/var/lib/rook`). Remove with `orcinus plugin remove storage --provider rook-ceph`.

> Not runnable everywhere — without raw devices/multiple nodes the CephCluster
> won't reach health. Tune the `CephCluster` for production (device filters,
> failure domains, replica size).

---

## Verifying & caveats

```bash
orcinus cluster status               # confirm your node count
orcinus ps <project>                 # see where pods landed
```

- **HA needs enough nodes.** A 3-replica volume needs ≥3 nodes; a 4-drive MinIO
  needs ≥4 drives (ideally on ≥4 nodes). Fewer nodes = it runs but isn't
  fault-tolerant.
- **Match replica count to nodes**, not more.
- **Back up anyway.** Replication is not backup; snapshot/export important data.
- **local-path is not HA** — it pins a volume to one node. Use it only for
  scratch/dev.

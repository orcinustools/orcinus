---
name: volumes-configs
description: Persistent volumes, host bind mounts, configs and secrets
tags: [volumes, configs]
---
- Named volume → PersistentVolumeClaim:  `data:/var/lib/x` (+ declare under top-level volumes:)
  size via `x-orcinus-volume-size: 5Gi` (or global --pvc-size).
- Bind mount → hostPath (node-local, like Swarm):  `./conf:/etc/app:ro` or `/srv/x:/data`.
- configs: / secrets: → ConfigMap / Secret mounted at the target. Relative `file:` paths
  resolve to the compose file's dir (deploy from the project dir).

Note: hostPath is node-local — on multi-node clusters use a PVC (networked StorageClass)
for data shared across nodes. See docs/USAGE.md §7.

---
name: debug
description: Inspect and troubleshoot deployments
tags: [debug]
---
    orcinus ls                         # projects + READY
    orcinus ps <project>               # pods, status, node
    orcinus logs <service> -f          # stream logs
    orcinus kubectl <args...>          # full kubectl passthrough (e.g. describe, get events)

Pod Pending? Usually an unsatisfiable placement/nodeSelector, a missing PVC
StorageClass, or an image pull error — check `orcinus kubectl describe pod <pod>`.
Preview what a file produces without touching the cluster: `orcinus deploy -f <f> --dry-run`.

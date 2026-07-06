---
name: scale-autoscale
description: Scale a service or enable HPA autoscaling
tags: [scale]
---
Manual scale:
    orcinus scale <service> <replicas> [-n <namespace>]

Autoscale (HPA) via CLI:
    orcinus autoscale <service> --min 2 --max 8 --cpu 70

Or declaratively in compose:
    x-orcinus-autoscale-min: 2
    x-orcinus-autoscale-max: 8
    x-orcinus-autoscale-cpu: 70
    deploy: { resources: { limits: { cpus: "0.5", memory: 256M } } }   # needed for CPU%

Verify: `orcinus kubectl get hpa`. Metrics come from the bundled metrics-server.

---
name: overview
description: What orcinus is and the golden path (read this first)
tags: [core]
---
Orcinus runs `docker-compose.yml` files (and raw Kubernetes manifests) on a real
Kubernetes cluster — no manifest rewriting. One binary.

Mental model:
- `orcinus deploy -f <file>` auto-detects each YAML doc as compose or manifest,
  converts compose → k8s objects, and applies with ownership labels.
- Compose/Swarm `deploy:` keys are honored (mode, placement, resources, update_config).
- Kubernetes-only extras use `x-orcinus-*` annotations (ingress/TLS, autoscale, rollout, …).

Golden path:
    orcinus cluster init                       # start a single-node cluster
    orcinus deploy -f docker-compose.yml --wait
    orcinus ls                                 # projects + readiness
    orcinus ps <project>                       # pods
    orcinus logs <service> -f
    orcinus rm <project>                       # remove (danger)
    orcinus cluster down                       # tear down cluster (danger)

Always safe to preview first:
    orcinus deploy -f <file> --dry-run         # print the k8s YAML, apply nothing

Discover more: `orcinus skills` (list), `orcinus skills <name>` (a recipe).
Full compatibility matrix: `docs/COMPOSE.md`. HTTP API: `orcinus api` (+ /openapi.json).

---
name: progressive-delivery
description: Canary or blue-green rollout for a service
tags: [rollout]
---
Add a rollout strategy; orcinus emits an Argo Rollout (auto-installs argo-rollouts):
    services:
      web:
        image: nginx:1.27
        ports: ["80"]
        x-orcinus-rollout: canary        # or: bluegreen

For plain rolling/recreate instead (Deployment):
    x-orcinus-strategy: recreate         # or rolling (default)
    deploy: { update_config: { order: start-first, parallelism: 2 } }

Roll back a bad release:
    orcinus rollback <service>
Verify: `orcinus kubectl get rollout <service>` → phase Healthy.

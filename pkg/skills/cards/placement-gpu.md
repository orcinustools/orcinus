---
name: placement-gpu
description: Pin pods to nodes (Swarm placement) and request GPUs
tags: [placement, gpu]
---
Node placement (Swarm deploy.placement → nodeAffinity/topologySpread):
    deploy:
      placement:
        constraints: [ "node.role == worker", "node.labels.zone == east", "node.platform.arch == amd64" ]
        preferences: [ { spread: node.labels.zone } ]
Label nodes first:  orcinus node ls ; orcinus node label <node> zone=east
Plain pin: x-orcinus-node-selector: { disktype: ssd }

GPU (docker-compose devices form):
    deploy: { resources: { reservations: { devices: [ { driver: nvidia, count: 1, capabilities: [gpu] } ] } } }
→ container gets nvidia.com/gpu limit. Enable GPUs on the cluster:
    orcinus plugin install nvidia-device-plugin       # needs GPU nodes + NVIDIA runtime
Unschedulable pods stay Pending — check `orcinus ps <project>`.

---
name: cluster-lifecycle
description: Create, join, inspect and tear down clusters
tags: [cluster]
danger: true
---
    orcinus cluster init [--http-port 80 --https-port 443] [--advertise <ip>]
    orcinus cluster join --server https://<ip>:6443 --token <token> [--role server|agent]
    orcinus cluster status
    orcinus cluster down                       # DANGER: removes the whole cluster + state

Runtime providers (--runtime):
- docker (default): runs in a container. Needs a container runtime.
- standalone: self-contained binary, runs the runtime natively (no container runtime;
  needs root + a real host). Build: `make orcinus-standalone`; or `install-standalone.sh`.
HA: init with --cluster-init (embedded etcd), then join --role server (odd count).

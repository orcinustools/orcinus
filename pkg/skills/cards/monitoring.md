---
name: runtime-monitoring
description: See commands run and files changed inside containers; kill shells as they start
tags: [security, monitoring, audit, runtime, tetragon]
danger: false
---
Turn on (Tetragon eBPF agents on every node; needs a kernel with BTF):
    orcinus monitor enable
    orcinus monitor status
Read events (default: last hour, all namespaces except kube-system):
    orcinus monitor                       # exec, session, blocked, write, truncate, delete, chmod
    orcinus monitor exec -s web -f        # commands in one service, live
    orcinus monitor files -n shop --since 24h
    orcinus monitor --json                # one JSON object per event, for a SIEM/Loki
`session` = someone ran kubectl/orcinus exec into the container.
Kill shells and package managers started in orcinus-deployed containers (app keeps running):
    orcinus monitor block-shell [--project p] [-n ns]
    orcinus monitor block-shell --off     # break-glass
Check first with `orcinus monitor exec`: an image whose entrypoint is a shell
script (nginx, postgres, … docker-entrypoint.sh) will not start under block-shell.
Remove: orcinus monitor disable. Docs: docs/MONITORING.md.

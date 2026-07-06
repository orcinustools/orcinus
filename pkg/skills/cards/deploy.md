---
name: deploy-app
description: Deploy a compose file or manifests to the cluster
tags: [deploy, core]
---
Deploy (convert compose + apply, or apply manifests):
    orcinus deploy -f docker-compose.yml --project myapp --wait

- With no -f, orcinus auto-discovers orcinus.yml / compose.yml in the current dir.
- --wait blocks until workloads are Ready; --project sets the ownership label.
- --prune (default on) removes owned resources that left the input.
- Preview without applying: add --dry-run (optionally -o out/ to write files).
- A file may mix compose services and raw k8s manifests (each doc auto-detected);
  put anything orcinus doesn't map as a raw manifest in the same file.

Verify: `orcinus ls` (READY column), `orcinus ps <project>`.
Update: edit the file and re-run deploy (idempotent).

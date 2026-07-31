---
name: inspect-export-config
description: See a deployed project's config and export it as k8s manifests or an orcinus.yml
tags: [deploy, inspect]
---
Read back what a project actually looks like on the cluster (not what your file
says — drift applied with kubectl shows up here too):

    orcinus config                          # summary of the project in this dir
    orcinus config <project> -n <namespace>

Export it:

    orcinus config <project> -o k8s      > k8s.yaml      # re-appliable manifests
    orcinus config <project> -o orcinus  > orcinus.yml   # rebuilt compose file

- `k8s` strips what belongs to one cluster (resourceVersion, uid, status,
  clusterIP, volumeName) so the output applies elsewhere.
- `orcinus` is best-effort: a cluster holds more than compose can express.
  Whatever was dropped is listed as `# note:` lines in the output header.
- Secret values are redacted unless `--show-secrets`; redacted output is not
  re-appliable as-is.

Use it to adopt a hand-applied app into a compose file, diff live state against
your file, or hand someone the manifests without giving them cluster access.

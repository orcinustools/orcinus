---
name: private-registry
description: Pull images from a private registry
tags: [registry, secrets]
danger: false
---
1) Log in (verifies credentials before storing the secret):
    orcinus secret create-registry regcred --server registry.example.com -u USER -p TOKEN
   Flags: --insecure (self-signed), --skip-login-check, -n <namespace>.

2) Attach it to the service:
    services:
      app:
        image: registry.example.com/team/app:1.0
        x-orcinus-image-pull-secret: regcred

Cluster-wide instead: attach to the namespace default ServiceAccount
    orcinus kubectl patch serviceaccount default -p '{"imagePullSecrets":[{"name":"regcred"}]}'
Verify: `orcinus ps <project>` shows the pod Running (image pulled).

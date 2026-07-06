---
name: secrets
description: Manage secrets and TLS certs
tags: [secrets]
---
Generic secret:      orcinus secret create app-config --from-literal FOO=bar
BYO TLS cert:        orcinus secret create-tls mysite-cert --cert fullchain.pem --key privkey.pem
                     then: x-orcinus-tls-secret: mysite-cert  on the service
Registry login:      orcinus secret create-registry ... (see skill: private-registry)
Move env vars into a Secret: x-orcinus-secret: [DB_PASSWORD] on the service.
List / delete:       orcinus secret ls ; orcinus secret rm <name>

---
name: secrets
description: Manage secrets and TLS certs
tags: [secrets]
---
Generic secret:      orcinus secret create app-secret --from-literal FOO=bar
                     then: x-orcinus-env-from-secret: app-secret  on the service
                     → every key in the Secret becomes an env var
BYO TLS cert:        orcinus secret create-tls mysite-cert --cert fullchain.pem --key privkey.pem
                     then: x-orcinus-tls-secret: mysite-cert  on the service
Registry login:      orcinus secret create-registry ... (see skill: private-registry)
Move env vars into a Secret: x-orcinus-secret: [DB_PASSWORD] on the service.
  (that CREATES a Secret from compose values — not for referencing an existing one)
Mount an existing Secret as a file instead: compose `secrets:` with `external: true`;
  the map key must equal the Secret's name, and it needs a data key of that name too.
List / delete:       orcinus secret ls ; orcinus secret rm <name>

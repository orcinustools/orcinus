---
name: secrets
description: Manage secrets and TLS certs
tags: [secrets]
---
Generic secret:      orcinus secret create app-secret --from-literal FOO=bar
                     then: x-orcinus-env-from-secret: app-secret  on the service
                     → every key in the Secret becomes an env var
From a file:         orcinus secret create certs --from-file tls.pem=./cert.pem
                     --from-file also takes a bare path (key = file name) or a
                     directory (one key per file). Raw bytes, so binary and
                     trailing newlines survive — unlike --from-literal "$(cat f)".
BYO TLS cert:        orcinus secret create-tls mysite-cert --cert fullchain.pem --key privkey.pem
                     then: x-orcinus-tls-secret: mysite-cert  on the service
Registry login:      orcinus secret create-registry ... (see skill: private-registry)
Move env vars into a Secret: x-orcinus-secret: [DB_PASSWORD] on the service.
  (that CREATES a Secret from compose values — not for referencing an existing one)
All from orcinus.yml, no CLI: a top-level `secrets: {apikey: {file: ./api.key}}`
  CREATES the Secret. Creating and consuming are separate — list it under a
  service's `secrets:` to mount it as a file, add x-orcinus-env-from-secret to
  load it as env vars, or do both. The data key is the secret's name, so the env
  var is named after the secret, not the file.
Reference a Secret this file did NOT create: compose `secrets:` with `external: true`;
  the map key must equal the Secret's name, and it needs a data key of that name too.
Inspect:             orcinus secret ls                  (names + key names)
                     orcinus secret get app-secret      (keys; values need --show-values)
Change one key:      orcinus secret set app-secret --from-literal DB_PASS=new
                     `create` REPLACES and drops unnamed keys; `set` merges.
                     Then: orcinus restart <service> — running pods keep old env values.
Delete:              orcinus secret rm <name>

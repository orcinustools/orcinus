---
name: expose-tls
description: Expose a service via Ingress with automatic HTTPS (Let's Encrypt)
tags: [ingress, tls]
---
Add ingress hints to the service, then deploy:
    services:
      web:
        image: nginx:1.27
        ports: ["80"]
        x-orcinus-expose: ingress
        x-orcinus-host: "app.example.com"     # comma-separated for multiple domains
        x-orcinus-tls: letsencrypt              # cert-manager ClusterIssuer

Prereqs: cluster reachable on 80/443 (orcinus cluster init --http-port 80 --https-port 443),
public DNS → the cluster. deploy auto-installs cert-manager if you pass --acme-email.
Path routing: x-orcinus-path: /api + x-orcinus-strip-prefix: true.
Traefik middlewares: x-orcinus-middleware: [name] (see `orcinus skills` → ingress in docs/INGRESS.md).
Verify: curl the host; check the served cert issuer is "Let's Encrypt".

# Orcinus — Ingress & Traefik Middlewares

Orcinus ships **Traefik** as the runtime's native ingress controller. This guide
covers how to expose services, do **path routing / prefix stripping**, and attach
**Traefik middlewares** (rate limit, headers, auth, redirects, CORS, and more).

For the command reference see [`USAGE.md`](./USAGE.md); for HTTPS/Let's Encrypt see
[`PLUGINS.md`](./PLUGINS.md).

---

## Table of Contents

- [1. Exposing a service](#1-exposing-a-service)
- [2. Path routing & prefix stripping](#2-path-routing--prefix-stripping)
- [3. Attaching Traefik middlewares](#3-attaching-traefik-middlewares)
- [4. Middleware catalog](#4-middleware-catalog)
  - [Path: StripPrefix / AddPrefix / ReplacePathRegex](#path-stripprefix--addprefix--replacepathregex)
  - [Redirects: scheme & regex](#redirects-scheme--regex)
  - [Headers & CORS](#headers--cors)
  - [Auth: basic / forward](#auth-basic--forward)
  - [Rate limit & in-flight](#rate-limit--in-flight)
  - [Compress, Retry, CircuitBreaker](#compress-retry-circuitbreaker)
  - [IP allow-list](#ip-allow-list)
  - [Chain](#chain)
- [5. How orcinus wires it (reference)](#5-how-orcinus-wires-it-reference)
- [6. Notes & gotchas](#6-notes--gotchas)

---

## 1. Exposing a service

Add ingress hints to a compose service with `x-orcinus-*` keys:

```yaml
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-expose: ingress        # create an Ingress for this service
    x-orcinus-host: app.example.com  # the host to route
    x-orcinus-path: /                # path (default "/")
    x-orcinus-port: 80               # backend service port (defaults from ports:)
    x-orcinus-ingress-class: traefik # ingress class (default: the cluster's)
    x-orcinus-tls: letsencrypt       # cert-manager ClusterIssuer (HTTPS) — see PLUGINS.md
```

| Key | Purpose |
|---|---|
| `x-orcinus-expose: ingress` | Generate an Ingress for the service |
| `x-orcinus-host` | Host to route (e.g. `app.example.com`) |
| `x-orcinus-path` | Path prefix (default `/`) |
| `x-orcinus-port` | Backend service port |
| `x-orcinus-ingress-class` | Ingress class (`traefik` by default in-cluster) |
| `x-orcinus-tls` / `x-orcinus-tls-secret` | HTTPS via cert-manager / an existing cert |
| **`x-orcinus-strip-prefix`** | Strip the path prefix (Traefik StripPrefix) — [§2](#2-path-routing--prefix-stripping) |
| **`x-orcinus-middleware`** | Attach Traefik middleware(s) by name — [§3](#3-attaching-traefik-middlewares) |

---

## 2. Path routing & prefix stripping

Route a service under a sub-path with `x-orcinus-path`:

```yaml
services:
  api:
    image: myapi:1.0
    ports: ["8080"]
    x-orcinus-expose: ingress
    x-orcinus-host: shop.example.com
    x-orcinus-path: /api            # https://shop.example.com/api → this service
```

The catch: the request still arrives at the backend as `/api/...`. Most apps
expect to receive `/...`. **`x-orcinus-strip-prefix`** fixes that — it generates a
Traefik **StripPrefix** middleware and attaches it, so the backend sees the path
with the prefix removed:

```yaml
    x-orcinus-path: /api
    x-orcinus-strip-prefix: true     # backend receives "/", not "/api"
```

`x-orcinus-strip-prefix` accepts:

| Form | Meaning |
|---|---|
| `true` | Strip whatever `x-orcinus-path` is (e.g. `/api`) |
| `"/api"` | Strip this explicit prefix |
| `["/v1", "/v2"]` | Strip any of these prefixes |

Orcinus generates a Middleware named `<service>-stripprefix` and lists it **first**
in the middleware chain (so everything downstream sees the stripped path).

> See [`examples/traefik-middleware/`](../examples/traefik-middleware/orcinus.yml).

---

## 3. Attaching Traefik middlewares

Traefik behaviour beyond stripping — rate limiting, auth, headers, redirects — is
expressed as **`Middleware`** custom resources. You define the middleware once,
then attach it to a service with **`x-orcinus-middleware`** (a name or a list, in
order):

```yaml
services:
  api:
    image: myapi:1.0
    ports: ["8080"]
    x-orcinus-expose: ingress
    x-orcinus-host: shop.example.com
    x-orcinus-middleware: [ratelimit, secure-headers]   # applied in this order

---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: ratelimit
spec:
  rateLimit: { average: 50, burst: 100 }
```

Because `orcinus deploy` classifies **each YAML document independently**, you can
keep the compose service and its `Middleware` CRDs **in the same file** (separated
by `---`) and deploy them together — no separate `kubectl apply` needed.

Order of the chain orcinus builds: **strip-prefix first** (if any), then the
`x-orcinus-middleware` list, left to right.

---

## 4. Middleware catalog

Common Traefik middlewares (`apiVersion: traefik.io/v1alpha1`). Define any of
these and attach with `x-orcinus-middleware: [<name>]`.

### Path: StripPrefix / AddPrefix / ReplacePathRegex

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: strip-api }
spec:
  stripPrefix:
    prefixes: ["/api"]        # remove a leading prefix (also via x-orcinus-strip-prefix)
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: add-api }
spec:
  addPrefix:
    prefix: "/api"            # prepend a prefix before hitting the backend
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: rewrite }
spec:
  replacePathRegex:
    regex: "^/old/(.*)"       # rewrite the path
    replacement: "/new/$1"
```

### Redirects: scheme & regex

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: https-redirect }
spec:
  redirectScheme:
    scheme: https
    permanent: true           # 301 http → https
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: apex-to-www }
spec:
  redirectRegex:
    regex: "^https://example\\.com/(.*)"
    replacement: "https://www.example.com/${1}"
    permanent: true
```

### Headers & CORS

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: secure-headers }
spec:
  headers:
    stsSeconds: 31536000
    stsIncludeSubdomains: true
    contentTypeNosniff: true
    browserXssFilter: true
    frameDeny: true
    referrerPolicy: same-origin
    customRequestHeaders:
      X-Forwarded-Proto: https
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: cors }
spec:
  headers:
    accessControlAllowMethods: ["GET", "POST", "OPTIONS"]
    accessControlAllowOriginList: ["https://app.example.com"]
    accessControlAllowHeaders: ["Content-Type", "Authorization"]
    accessControlMaxAge: 600
    addVaryHeader: true
```

### Auth: basic / forward

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: dashboard-auth }
spec:
  basicAuth:
    secret: dashboard-users   # Secret with `users` = htpasswd lines (bcrypt)
---
# Create the secret:  htpasswd -nbB admin 's3cret'  → put the line under stringData.users
apiVersion: v1
kind: Secret
metadata: { name: dashboard-users }
stringData:
  users: |
    admin:$2y$05$....(bcrypt)....
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: forward-auth }
spec:
  forwardAuth:
    address: http://authelia.default.svc:9091/api/verify
    authResponseHeaders: ["Remote-User", "Remote-Groups"]
```

### Rate limit & in-flight

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: ratelimit }
spec:
  rateLimit:
    average: 50               # req/s sustained
    burst: 100                # short bursts
    period: 1s
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: inflight }
spec:
  inFlightReq:
    amount: 20                # max simultaneous in-flight requests
```

### Compress, Retry, CircuitBreaker

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: gzip }
spec:
  compress: {}
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: retry }
spec:
  retry:
    attempts: 3
    initialInterval: 100ms
---
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: breaker }
spec:
  circuitBreaker:
    expression: "NetworkErrorRatio() > 0.30"
```

### IP allow-list

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: office-only }
spec:
  ipAllowList:                # Traefik v3 key (v2 used `ipWhiteList`)
    sourceRange: ["10.0.0.0/8", "203.0.113.0/24"]
```

### Chain

Group several middlewares into one reusable unit:

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata: { name: hardened }
spec:
  chain:
    middlewares:
      - name: https-redirect
      - name: secure-headers
      - name: ratelimit
```

Then just `x-orcinus-middleware: [hardened]`.

---

## 5. How orcinus wires it (reference)

Under the hood orcinus works with standard Kubernetes Ingress + Traefik's
Kubernetes-CRD provider:

- `x-orcinus-middleware` / `x-orcinus-strip-prefix` set the annotation on the
  generated Ingress:

  ```yaml
  metadata:
    annotations:
      traefik.ingress.kubernetes.io/router.middlewares: <ns>-<name>@kubernetescrd,...
  ```

  The reference is **namespace-qualified** and suffixed `@kubernetescrd`. Orcinus
  fills in the deploy namespace for you (defaulting to `default`).

- `x-orcinus-strip-prefix` additionally **generates** a `StripPrefix` Middleware
  named `<service>-stripprefix`, carrying orcinus ownership labels so `rm`/prune
  clean it up with the rest of the project.

- Middlewares referenced by name in `x-orcinus-middleware` must exist in the
  **same namespace** as the service (define them in the same file).

---

## 6. Notes & gotchas

- **Traefik must be enabled.** It is by default. If you started the cluster with
  Traefik disabled (`--server-arg --disable=traefik`), install an ingress
  controller or don't use these keys.
- **API version.** Traefik v3 (current) uses `traefik.io/v1alpha1`. Older Traefik
  v2 used `traefik.containo.us/v1alpha1` and `ipWhiteList` instead of
  `ipAllowList`. Use the group that matches your Traefik.
- **Order matters.** The chain is: auto strip-prefix (if any) → your
  `x-orcinus-middleware` list, left to right. Put redirects/auth before the app.
- **Cross-namespace.** To attach a middleware from another namespace, reference it
  as `<other-ns>-<name>@kubernetescrd` in a raw Ingress; the `x-orcinus-middleware`
  sugar assumes the service's own namespace.
- **IngressRoute vs Ingress.** Orcinus generates standard `Ingress` objects (+ the
  Traefik annotation), which covers the vast majority of cases. For advanced
  Traefik-only routing (TCP/UDP routers, weighted services) write a raw
  `IngressRoute` manifest and deploy it in the same file.

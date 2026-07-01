# Orcinus — Command-Line Structure

> CLI interface specification.
>
> Philosophy: **as pleasant as Docker Swarm.** Few commands, familiar verbs
> (`init`, `join`, `deploy`, `ls`, `ps`, `rm`), no ceremony.
> Architecture reference: `ARCHITECTURE.md`. The binary is **multicall**.

---

## 1. UX Principles

1. **One command to deploy anything.** `orcinus deploy -f <file>` **detects on its
   own** whether the file is docker-compose or a Kubernetes manifest:
   - **compose** → converted *on the fly* in memory, created directly.
   - **k8s manifest** → applied as-is.
   There is no mandatory separate `convert` step. There is no separate
   manifest-only deploy command. One door.
2. **Swarm-flavored verbs.** People used to `docker stack deploy` / `docker service
   ls` understand it immediately.
3. **kubectl is optional.** Still available as an escape hatch, but not the main path.

Feel mapping:

| Docker Swarm | Orcinus |
|---|---|
| `docker swarm init` | `orcinus init` |
| `docker swarm join` | `orcinus join` |
| `docker stack deploy -c f.yml app` | `orcinus deploy -f f.yml` |
| `docker service ls` | `orcinus ls` |
| `docker service ps <svc>` | `orcinus ps` |
| `docker service logs <svc>` | `orcinus logs <svc>` |
| `docker stack rm app` | `orcinus rm <project>` |
| `docker node ls` | `orcinus node ls` |

---

## 2. Command Tree

```
orcinus
├── init                  # make this node a control plane (≈ swarm init)
├── join                  # join a cluster as a node (≈ swarm join)
├── deploy                # ⭐ auto-detect compose|manifest → deploy directly
├── rm                    # remove orcinus-managed projects/resources
├── ls                    # list running services/projects
├── ps                    # list a project's pods/tasks
├── logs                  # service logs
├── node                  # manage nodes       (subgroup: ls, rm)
├── token                 # node join tokens   (subgroup: create, rotate)
├── kubectl               # (optional) built-in kubectl, passthrough
├── version
└── completion
```

**Removed** from the earlier draft:
- ❌ `convert` as a mandatory step → folded into detection inside `deploy`.
- ❌ `up`/`down` → replaced by `deploy`/`rm` (more Swarm-like).
- ❌ `crictl`/`ctr` as primary commands → not surfaced (available via kubectl if needed).

---

## 3. Core Commands

### 3.1 `orcinus init` — start a cluster
Makes this node a control plane (runs the control plane + workloads).

```bash
orcinus init [flags]
```

| Flag | Default | Description |
|---|---|---|
| `--bind-address <ip>` | `0.0.0.0` | API server bind address |
| `--port <n>` | `6443` | API server port |
| `--token <str>` | auto | Join token for other nodes |
| `--cluster-init` | `false` | Embedded etcd (HA mode) — see §5 |
| `--datastore-endpoint <str>` | SQLite | External datastore (etcd/Postgres/MySQL) — see §5 |
| `--datastore-cafile <path>` | — | Datastore server CA (TLS) — see §5.5 |
| `--datastore-certfile <path>` | — | Datastore client certificate (mTLS) — see §5.5 |
| `--datastore-keyfile <path>` | — | Datastore client private key (mTLS) — see §5.5 |

After `init`, it prints a ready-to-paste `orcinus join ...` command (Swarm-style).

### 3.2 `orcinus join` — add a node
```bash
orcinus join --server https://<ip>:6443 --token <token>
```

### 3.3 `orcinus deploy` — ⭐ the main command
Auto-detection + on-the-fly deploy. **One command for both compose and manifests.**

```bash
orcinus deploy -f docker-compose.yml          # compose → convert on the fly → create
orcinus deploy -f k8s-manifest.yaml           # manifest → apply directly
orcinus deploy -f a.yml -f b.yaml             # mix several files
cat manifest.yaml | orcinus deploy -f -       # from stdin
```

Detection logic (per YAML document) — **default: auto-detect**:
- Has `apiVersion` **and** `kind` → **k8s manifest** → apply directly.
- Has a top-level `services:` (optional `version:`) → **compose** → convert then create.
- Neither → a clear error ("neither compose nor a k8s manifest").
- Multi-document files may mix both; each document is detected independently.

**Force the mode with `--as` (optional).** For ambiguous cases or CI that wants
determinism, auto-detection can be bypassed:
- `--as compose`  → all input documents are treated as compose.
- `--as manifest` → all input documents are treated as k8s manifests.
- without the flag (default) → auto-detect as above.
So both are supported: **auto-detect** for convenience, **`--as`** for certainty.

| Flag | Default | Description |
|---|---|---|
| `-f, --file <path>` (repeatable, `-`=stdin) | `docker-compose.yml` | Input file |
| `-n, --namespace <ns>` | `default` | Target namespace |
| `--project <name>` | directory name | Ownership label (for `ls`/`rm`) |
| `--kubeconfig <path>` | auto | Target cluster (default: local orcinus cluster) |
| `--wait` | `false` | Wait until resources are ready |
| `--prune` | `true` | Remove old resources no longer present in the input |
| `--dry-run` | `false` | Show the diff / detection result, do not apply |
| `--as <compose\|manifest>` | auto | Force the input mode; empty = auto-detect |
| `-o, --output <dir>` | — | (optional) also write converted manifests to a dir |

> Note: `--output` replaces the need for a separate `convert` command — if someone
> just wants manifests without deploying, use `deploy --dry-run -o`.

### 3.4 `orcinus rm` — tear down
```bash
orcinus rm <project>                 # remove all of a project's resources
orcinus rm -f docker-compose.yml     # infer the project from the file
```

### 3.5 `orcinus ls` / `ps` / `logs`
```bash
orcinus ls                    # list projects + summary (managed-by=orcinus)
orcinus ps <project>          # a project's pods/tasks
orcinus logs <service> [-f]   # service logs (follow with -f)
```

### 3.6 `orcinus node`
```bash
orcinus node ls
orcinus node rm <name>
```

### 3.7 `orcinus token`
```bash
orcinus token create
orcinus token rotate
```

---

## 4. Optional: `orcinus kubectl`
An escape hatch for power users; full passthrough to the built-in kubectl.

```bash
orcinus kubectl get pods -A
orcinus kubectl describe deploy web
```

---

## 5. Datastore

Default **SQLite**; other backends are opt-in. **FINAL: Option A — pass through
k3s flags.** Rationale: consistent with the "k3s as a library" philosophy
(ARCHITECTURE §D1), without an abstraction layer that must be kept in sync with
upstream k3s. Option B (an `--datastore etcd|postgres` abstraction) is **rejected**
for simplicity.

> Everything in this section applies to **M3** (the k3s runtime), not M0–M2.

### 5.1 Background: how k3s stores state

Kubernetes needs a datastore that speaks the **etcd API**. k3s offers two paths:

- **Native etcd** — used for embedded mode (`--cluster-init`) and external etcd.
- **kine** (*"kine is not etcd"*) — a shim that **impersonates etcd** but stores
  data in a relational database: **SQLite, PostgreSQL, MySQL/MariaDB**. Thanks to
  kine, k3s can run without etcd at all.

Consequence: the datastore choice determines **HA (high availability) capability**,
not just where data is stored.

### 5.2 Supported modes

| Mode | Backing | HA? | Usage |
|---|---|---|---|
| **SQLite** (default) | local file via kine | ❌ no | `orcinus init` |
| **etcd embedded** | native etcd inside the node | ✅ yes (odd node count: 3/5/…) | `orcinus init --cluster-init` |
| **etcd external** | your own etcd cluster | ✅ yes (self-managed) | `--datastore-endpoint https://etcd:2379` |
| **PostgreSQL** | Postgres via kine | ✅ yes (HA at the DB layer) | `--datastore-endpoint "postgres://…"` |
| **MySQL / MariaDB** | MySQL via kine | ✅ yes (HA at the DB layer) | `--datastore-endpoint "mysql://…"` |

### 5.3 Example commands per mode

```bash
# SQLite — default, single node, dev/edge/homelab
orcinus init

# etcd embedded — HA without an external DB (run on node 1)
orcinus init --cluster-init
#   subsequent control-plane nodes join the same etcd cluster:
orcinus init --server https://<node1>:6443 --token <token>

# external etcd — you already have a managed etcd cluster
orcinus init --datastore-endpoint "https://etcd-1:2379,https://etcd-2:2379,https://etcd-3:2379"

# PostgreSQL via kine
orcinus init --datastore-endpoint "postgres://user:pass@pg-host:5432/orcinus?sslmode=require"

# MySQL / MariaDB via kine
orcinus init --datastore-endpoint "mysql://user:pass@tcp(db-host:3306)/orcinus"
```

### 5.4 Endpoint string format (`--datastore-endpoint`)

| Backend | Scheme / form |
|---|---|
| external etcd | `https://host:2379` (multiple allowed, comma-separated) |
| PostgreSQL | `postgres://user:password@host:port/dbname[?sslmode=…]` |
| MySQL / MariaDB | `mysql://user:password@tcp(host:port)/dbname` |
| SQLite (explicit) | `sqlite:///var/lib/orcinus/state.db` (optional; default when the flag is empty) |

### 5.5 Datastore with TLS (external etcd / SSL DB)

Endpoints that require certificates use three extra flags — passed through to k3s
as-is:

| Flag | Description |
|---|---|
| `--datastore-cafile <path>` | CA to verify the datastore server |
| `--datastore-certfile <path>` | Client certificate (mTLS) |
| `--datastore-keyfile <path>` | Client private key (mTLS) |

```bash
orcinus init \
  --datastore-endpoint "https://etcd-1:2379,https://etcd-2:2379" \
  --datastore-cafile   /etc/orcinus/etcd/ca.crt \
  --datastore-certfile /etc/orcinus/etcd/client.crt \
  --datastore-keyfile  /etc/orcinus/etcd/client.key
```

### 5.6 Selection guide (short)

- **Dev / single node / edge** → **SQLite** (default). Lightest, zero dependencies.
- **Most upstream-supported HA** → **etcd embedded** (`--cluster-init`), needs an
  **odd** number of control-plane nodes (3 or 5) for quorum.
- **Already have managed etcd** → **external etcd**.
- **Already have managed Postgres/MySQL and want DB-based HA** → **kine +
  Postgres/MySQL**. Note: the kine path is relatively less tested than embedded
  etcd for heavy HA workloads.

### 5.7 Important notes

- **SQLite is not HA.** For more than one control-plane node, use embedded etcd or
  an external datastore.
- **Backup:** SQLite/external DB → back up at the DB level; embedded etcd → k3s
  provides automatic etcd snapshots (we pass the relevant flags through when
  enabled in a later milestone).
- **Endpoint consistency:** all control-plane nodes must point at the same
  datastore; worker nodes (`orcinus join`) do not need to know the datastore, just
  `--server`.
- **Security:** DB credentials in `--datastore-endpoint` should come from an
  env/secret, not be hard-coded in scripts (they end up in shell history).

---

## 6. Full Flow Example (Swarm feel)

```bash
# 1. Start the cluster
orcinus init                       # prints a ready-to-paste join command

# 2. Deploy — any file, one command
orcinus deploy -f docker-compose.yml --wait     # compose, auto-converted
orcinus deploy -f extra-ingress.yaml            # k8s manifest, applied directly

# 3. Inspect
orcinus ls
orcinus ps myapp
orcinus logs web -f

# 4. Tear down
orcinus rm myapp
```

---

## 7. Finalized Decisions

1. **Main command name** → **`deploy`**. ✅
2. **Tear-down** → **`rm`** (not `down`). ✅
3. **Datastore (§5)** → **Option A** (pass through k3s flags). ✅
4. **Input detection** → **both**: auto-detect (default) **and** a forcing flag
   `--as compose|manifest` (§3.3). ✅
5. **Architecture (D1/D4)** → k3s & kubectl **imported as libraries**; kompose is
   **forked** for full Docker Compose compatibility (see `ARCHITECTURE.md` §3). ✅

> All review questions are answered. Implementation status is tracked in
> `ARCHITECTURE.md` §10 (roadmap).

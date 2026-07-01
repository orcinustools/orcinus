# Orcinus — Command-Line Structure

> CLI interface specification.
>
> Philosophy: **as pleasant as Docker Swarm.** Few commands, familiar verbs
> (`init`, `join`, `deploy`, `ls`, `ps`, `rm`), no ceremony.
> Architecture reference: [`ARCHITECTURE.md`](./ARCHITECTURE.md). The binary is
> **multicall**.

---

## 1. UX Principles

1. **One command to deploy anything.** `orcinus deploy -f <file>` detects on its
   own whether the file is docker-compose or a Kubernetes manifest:
   - **compose** → converted on the fly in memory, then applied.
   - **manifest** → applied as-is.
   No mandatory separate `convert` step. One door.
2. **Swarm-flavored verbs.** People used to `docker stack deploy` understand it.
3. **Direct manifest access is optional**, not the main path.

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

---

## 2. Command Tree

```
orcinus
├── init                  # make this node a control plane (≈ swarm init)
├── join                  # join a cluster as a node (≈ swarm join)
├── deploy                # ⭐ auto-detect compose|manifest → deploy
├── rm                    # remove orcinus-managed projects/resources
├── ls                    # list running services/projects
├── ps                    # list a project's pods/tasks
├── logs                  # service logs
├── version
└── completion
```

---

## 3. Core Commands

### 3.1 `orcinus init` — start a cluster
Makes this node a control plane (runs the control plane + workloads).

| Flag | Default | Description |
|---|---|---|
| `--bind-address <ip>` | `0.0.0.0` | API server bind address |
| `--port <n>` | `6443` | API server port |
| `--token <str>` | auto | Join token for other nodes |
| `--cluster-init` | `false` | Embedded etcd (HA mode) — see §5 |
| `--datastore-endpoint <str>` | SQLite | External datastore — see §5 |
| `--datastore-cafile <path>` | — | Datastore server CA (TLS) — see §5.4 |
| `--datastore-certfile <path>` | — | Datastore client certificate (mTLS) — see §5.4 |
| `--datastore-keyfile <path>` | — | Datastore client private key (mTLS) — see §5.4 |

`init` writes the cluster kubeconfig to `~/.orcinus/kubeconfig` and records
cluster state, so `deploy`/`ls`/`ps`/`logs`/`rm` work afterwards with **no
`--kubeconfig`**. It then prints a ready-to-paste `orcinus join ...` command.

### 3.2 `orcinus join` — add a node
```bash
orcinus join --server https://<ip>:6443 --token <token>
orcinus join                     # on the init host: reads saved cluster state
```

### 3.3 `orcinus deploy` — ⭐ the main command
Auto-detection + deploy. **One command for both compose and manifests.**

```bash
orcinus deploy                                # no -f: auto-discover a default file
orcinus deploy -f docker-compose.yml          # compose → convert → apply
orcinus deploy -f k8s-manifest.yaml           # manifest → apply directly
orcinus deploy -f a.yml -f b.yaml             # mix several files
cat manifest.yaml | orcinus deploy -f -       # from stdin
```

**Default file discovery.** With no `-f`, orcinus looks in the current directory,
in priority order: `orcinus.yml`, `orcinus.yaml`, `compose.yaml`, `compose.yml`,
`docker-compose.yml`, `docker-compose.yaml` — and uses the first it finds. So an
`orcinus.yml` in your project is picked up automatically.

Detection logic (per YAML document) — **default: auto-detect**:
- Has `apiVersion` **and** `kind` → **manifest** → apply directly.
- Has a top-level `services:` → **compose** → convert then apply.
- Neither → a clear error.
- Multi-document files may mix both; each document is detected independently.

**Force the mode with `--as`** (`compose` | `manifest`) for ambiguous cases or
deterministic CI. Without it, auto-detection applies.

| Flag | Default | Description |
|---|---|---|
| `-f, --file <path>` (repeatable, `-`=stdin) | auto-detect | Input file (default: discover, see above) |
| `-n, --namespace <ns>` | `default` | Target namespace |
| `--project <name>` | directory name | Ownership label (for `ls`/`rm`/prune) |
| `--kubeconfig <path>` | auto | Target cluster |
| `--wait` | `false` | Wait until workloads are ready |
| `--prune` | `true` | Remove owned resources no longer in the input |
| `--dry-run` | `false` | Render instead of applying |
| `--as <compose\|manifest>` | auto | Force the input mode |
| `-o, --output <dir>` | — | Also write converted manifests to a dir |

> `--output` replaces a separate `convert` command — for manifests without
> deploying, use `deploy --dry-run -o`.

### 3.4 `orcinus rm` — tear down
```bash
orcinus rm <project>                 # remove all of a project's resources
```

### 3.5 `orcinus ls` / `ps` / `logs`
```bash
orcinus ls                    # list projects (managed-by=orcinus)
orcinus ps <project>          # a project's pods/tasks
orcinus logs <service> [-f]   # service logs
```

---

## 4. Full Flow Example

```bash
orcinus init                                    # start the cluster
orcinus deploy -f docker-compose.yml --wait     # compose, auto-converted & applied
orcinus deploy -f extra-ingress.yaml            # manifest, applied directly
orcinus ls
orcinus rm myapp                                # tear down
```

---

## 5. Datastore

Orcinus stores cluster state in a datastore. The default is **SQLite**; other
backends are opt-in via `orcinus init` flags.

> This section applies to the cluster runtime (`orcinus init`), not to `deploy`.

### 5.1 Supported modes

| Mode | HA? | Usage |
|---|---|---|
| **SQLite** (default) | ❌ no | `orcinus init` |
| **etcd embedded** | ✅ yes (odd node count: 3/5/…) | `orcinus init --cluster-init` |
| **etcd external** | ✅ yes (self-managed) | `--datastore-endpoint https://etcd:2379` |
| **PostgreSQL** | ✅ yes (HA at the DB layer) | `--datastore-endpoint "postgres://…"` |
| **MySQL / MariaDB** | ✅ yes (HA at the DB layer) | `--datastore-endpoint "mysql://…"` |

### 5.2 Examples

```bash
orcinus init                                                          # SQLite
orcinus init --cluster-init                                           # embedded etcd (HA)
orcinus init --datastore-endpoint "https://etcd-1:2379,https://etcd-2:2379"
orcinus init --datastore-endpoint "postgres://user:pass@pg:5432/orcinus?sslmode=require"
orcinus init --datastore-endpoint "mysql://user:pass@tcp(db:3306)/orcinus"
```

### 5.3 Endpoint string format (`--datastore-endpoint`)

| Backend | Scheme / form |
|---|---|
| external etcd | `https://host:2379` (comma-separated for multiple) |
| PostgreSQL | `postgres://user:password@host:port/dbname[?sslmode=…]` |
| MySQL / MariaDB | `mysql://user:password@tcp(host:port)/dbname` |
| SQLite (explicit) | `sqlite:///var/lib/orcinus/state.db` |

### 5.4 Datastore with TLS

| Flag | Description |
|---|---|
| `--datastore-cafile <path>` | CA to verify the datastore server |
| `--datastore-certfile <path>` | Client certificate (mTLS) |
| `--datastore-keyfile <path>` | Client private key (mTLS) |

### 5.5 Notes

- **SQLite is not HA.** For more than one control-plane node, use embedded etcd or
  an external datastore.
- **Endpoint consistency:** all control-plane nodes must point at the same
  datastore; worker nodes (`orcinus join`) only need `--server`.
- **Security:** put DB credentials in an env/secret, not hard-coded in scripts.

---

## 6. Finalized Decisions

1. **Main command name** → **`deploy`**. ✅
2. **Tear-down** → **`rm`** (not `down`). ✅
3. **Datastore** → pass configuration through `orcinus init` flags. ✅
4. **Input detection** → both auto-detect (default) and a `--as` forcing flag. ✅
5. **Conversion engine** → **forked kompose** for full Docker Compose
   compatibility (see [`ARCHITECTURE.md`](./ARCHITECTURE.md) §3). ✅

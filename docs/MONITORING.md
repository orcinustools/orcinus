# Runtime monitoring — commands and file changes in containers

`orcinus monitor` shows what actually runs inside your containers: every
command executed, every `kubectl exec` / `orcinus exec` session, and every file
written, truncated, deleted or chmod-ed. `orcinus monitor block-shell` goes one
step further and kills shells and package managers the moment they start.

It is built on [Tetragon](https://tetragon.io) (Cilium, eBPF). An agent on each
node watches the kernel itself, so it sees processes in every container — also
ones with no shell or tools — and nothing inside a container can switch it off
or hide from it.

- [How it fits with signing and immutable images](#how-it-fits)
- [Turn it on](#turn-it-on)
- [Reading events](#reading-events)
- [Blocking shells](#blocking-shells)
- [Walkthrough: catching a remote code execution](#walkthrough-catching-a-remote-code-execution)
- [Sending events elsewhere (SIEM, Loki)](#sending-events-elsewhere)
- [What is watched, exactly](#what-is-watched-exactly)
- [Limitations](#limitations)
- [Troubleshooting](#troubleshooting)
- [Turning it off](#turning-it-off)

## How it fits

| Layer | Command | Question it answers |
|---|---|---|
| Signing | `orcinus sign` ([SIGNING.md](./SIGNING.md)) | *Which images may run?* Only signed, digest-pinned ones. |
| Immutable images | [`orcinus/base`](../images/base/README.md), `read_only`, `cap_drop`, `no-new-privileges` | *What can an attacker do inside?* Very little: no shell, no tools, no writes. |
| Runtime monitoring | `orcinus monitor` | *What actually ran, and what changed?* Recorded from the kernel. |
| Shell blocking | `orcinus monitor block-shell` | *Can someone still get a shell?* No — it is killed on start. |

On an image built from `orcinus/base`, any command other than your app, and any
file write, is suspicious by definition: the image has nothing else to run.

## Turn it on

```bash
orcinus monitor enable      # installs the tetragon plugin, waits for the node agents
orcinus monitor status
```
```
agents:        1/1 nodes ready
file changes:  on
shell block:   off
```

`enable` is the same as `orcinus plugin install tetragon` plus a wait for the
agents. It works on both runtimes (docker and standalone). Nodes need a kernel
with BTF (`ls /sys/kernel/btf/vmlinux`) — any distribution kernel from the last
few years (Ubuntu 20.10+, Debian 11+, RHEL/Alma/Rocky 8.2+).

The agents run in the `tetragon` namespace, labelled as a plugin namespace, so
image signing lets their upstream images run.

## Reading events

```bash
orcinus monitor                        # everything from the last hour
orcinus monitor -f                     # ... and keep following
orcinus monitor exec                   # commands only (exec, session, blocked)
orcinus monitor files                  # file changes only
orcinus monitor exec -s web            # one compose service
orcinus monitor -n shop --since 24h    # one namespace, last day
orcinus monitor --json                 # one JSON object per line
```

```
2026-11-10 12:16:48  mon-e2e/rce-7d6bb8d5b5-r48dj   exec     uid=0     /bin/sh -c id>/tmp/pwned
2026-11-10 12:16:48  mon-e2e/rce-7d6bb8d5b5-r48dj   exec     uid=0     /usr/bin/id
2026-11-10 12:16:48  mon-e2e/rce-7d6bb8d5b5-r48dj   write    uid=0     /tmp/pwned  by /usr/bin/id
2026-11-10 12:16:48  mon-e2e/rce-7d6bb8d5b5-r48dj   chmod    uid=0     /tmp/pwned → 0777  by /bin/chmod
2026-11-10 12:16:48  mon-e2e/rce-7d6bb8d5b5-r48dj   delete   uid=0     /tmp/pwned  by /bin/rm
2026-11-10 12:16:49  mon-e2e/rce-7d6bb8d5b5-r48dj   session  uid=0     a shell or command was started from outside (kubectl/orcinus exec)
2026-11-10 12:17:47  mon-e2e/rce-7d6bb8d5b5-r48dj   blocked  uid=0     /bin/sh  by /usr/local/bin/python  (killed by block-shell)
```

| Kind | Meaning |
|---|---|
| `exec` | A program started in the container. `--json` adds its parent process. |
| `session` | Someone entered the container from outside (`kubectl exec`, `orcinus exec`). The commands they run follow as `exec`. |
| `blocked` | `block-shell` killed a shell or package manager as it started. `by` is the process that tried. |
| `write` | A file was written. Reported once per process and file per minute, so an app appending to a log does not flood the stream. |
| `truncate` | A file was truncated. |
| `delete` | A file was deleted. |
| `chmod` | A file's mode changed (new mode shown in octal). |

`--json` fields: `time`, `node`, `kind`, `namespace`, `pod`, `container`,
`service`, `image`, `binary`, `args`, `path`, `mode`, `uid`, `parent`.

How far back you can look depends on what the agents still hold: each node
keeps up to ~60 MB of events (10 MB × 5 rotated files), and the stream through
`orcinus monitor` holds what the kubelet keeps of the agent's log. For
retention, ship events elsewhere ([below](#sending-events-elsewhere)).

## Blocking shells

```bash
orcinus monitor block-shell                       # every service orcinus deployed
orcinus monitor block-shell --project shop        # only these projects (repeatable)
orcinus monitor block-shell -n prod               # only these namespaces
orcinus monitor block-shell --off
```

From then on, in those containers, the kernel kills any process that starts:

```
sh bash dash ash zsh ksh csh tcsh fish busybox      (shells)
apt apt-get dpkg apk dnf yum microdnf rpm           (package managers)
```

— whoever starts it: the app itself (a remote code execution calling
`system()`), or a person with `kubectl exec`. The app keeps running; only the
new process dies. The attempt shows as `blocked` in `orcinus monitor exec`.

Scope:

- Only pods **orcinus deployed** are affected (label
  `app.kubernetes.io/managed-by=orcinus`). The cluster's own pods and plugins —
  several run shell scripts — are never touched, and neither are pods created
  with plain `kubectl`.
- New projects are covered as soon as they are deployed; there is nothing to
  re-run.
- Running `block-shell` again replaces the scope; it does not add to it.

Before you turn it on:

- **An image whose entrypoint is a shell script will not start.** Many official
  images (`nginx`, `postgres`, `redis`, …) start through `docker-entrypoint.sh`;
  their containers would be killed on the next restart. Check with
  `orcinus monitor exec` first: if a service runs `sh` on its own, leave it out
  with `--project`/`-n`, or move it to a shell-free image such as
  [`orcinus/base`](../images/base/README.md).
- An app that calls `system()`/`popen()`/`exec.Command("sh", …)` on purpose
  gets those calls killed (the call fails with signal 9).
- `orcinus exec <service> sh` stops working for those services. Use
  `orcinus monitor block-shell --off` as the break-glass.

Matching is on the path the process asks to run (`/bin/sh`, `/usr/bin/apk`),
not on the file behind it — on busybox images `/bin/cat` is busybox too and
keeps working, while `/bin/sh` and a direct `/bin/busybox …` are killed.

## Walkthrough: catching a remote code execution

[`examples/monitor-demo`](../examples/monitor-demo/README.md) is a deliberately
vulnerable web service: it runs whatever is in `?cmd=` through `os.system`.

```bash
cd examples/monitor-demo
orcinus kubectl create namespace monitor-demo
orcinus deploy -n monitor-demo
orcinus monitor enable

# The attacker:
POD_IP=$(orcinus kubectl -n monitor-demo get pod -l io.kompose.service=rce -o jsonpath='{.items[0].status.podIP}')
orcinus kubectl -n monitor-demo run attacker --rm -i --restart=Never --image=alpine:3.20 -- \
  wget -qO- "http://$POD_IP:8080/?cmd=id>/tmp/pwned;chmod+777+/tmp/pwned"

# What happened:
orcinus monitor -n monitor-demo -s rce --since 5m
```

You see `/bin/sh -c id>/tmp/pwned…` started by python, `id` writing
`/tmp/pwned`, and the chmod. Now block it:

```bash
orcinus monitor block-shell --project monitor-demo
orcinus kubectl -n monitor-demo run attacker --rm -i --restart=Never --image=alpine:3.20 -- \
  wget -qO- "http://$POD_IP:8080/?cmd=id"          # rc=9: the shell was killed
orcinus monitor exec -n monitor-demo --since 1m    # → blocked  /bin/sh  by /usr/local/bin/python
```

The service is still up and still answering.

## Sending events elsewhere

The agents write JSON lines. Two ways to keep them:

- **Pipe the CLI** — `orcinus monitor -f --json` emits one normalised event per
  line (the fields above); feed it to `vector`, `fluent-bit`, `jq`, or a file.
- **Collect the agent log** — every node's `tetragon` pod has a container
  `export-stdout` that prints Tetragon's raw events. Any log shipper that reads
  pod logs (Promtail/Alloy for Loki, Fluent Bit, the Datadog agent, …) picks
  them up; filter on namespace `tetragon`, container `export-stdout`. The raw
  format is documented at <https://tetragon.io/docs/concepts/events/>.

The agents also serve Prometheus metrics on port 2112 (service `tetragon` in namespace `tetragon`)
(`tetragon_events_total`, `tetragon_policy_events_total`, …).

## What is watched, exactly

Two Tetragon `TracingPolicy` objects:

| Policy | Installed by | Hooks | Scope |
|---|---|---|---|
| `orcinus-file-changes` | `monitor enable` | `security_file_permission` (writes), `security_path_truncate`, `security_path_unlink`, `security_path_chmod` | All pods except `kube-system`, `kube-public`, `kube-node-lease`, `tetragon`. Writes to `/proc`, `/sys`, `/dev`, pipes and sockets are skipped. |
| `orcinus-block-shell` | `monitor block-shell` | `sys_execve`, action `Sigkill` | Pods with `app.kubernetes.io/managed-by=orcinus`, narrowed by `--project`/`-n`. |

Process execution is recorded by Tetragon for every container without a
policy. Events from the host itself, `kube-system` and `tetragon`, and
Kubernetes health-check probes are not exported.

See them with `orcinus kubectl get tracingpolicies`.

## Limitations

- **Renamed binaries.** `block-shell` matches program names. An attacker who can
  write and run a copy of a shell under another name gets past it — which is why
  it belongs together with read-only root filesystems (`read_only: true`) and a
  shell-free image, where there is no shell to copy and nowhere to put it.
- **No file contents.** File events say which file and which process, not what
  was written.
- **Renames are not reported** (only write, truncate, delete, chmod).
- **A blocked `kubectl exec` shows as a `session` only.** When the command
  started by `kubectl exec` is itself the shell, the kill happens before Tetragon
  has the process on record, so there is no `blocked` line; the `session` line
  and the exit code 137 the user sees are the trace. Shells started by the app
  show as `blocked`.
- **The agents need root on the node** (they load eBPF programs). Both runtimes
  already run as root.
- **Docker runtime:** the node container does not share `/sys/fs/bpf` with the
  host, so eBPF maps do not survive an agent restart — a restarted agent starts
  watching again from scratch (a few seconds of gap).

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `runtime monitoring is not installed` | Run `orcinus monitor enable`. |
| `agents: 0/1 nodes ready` | `orcinus kubectl -n tetragon get pods`; then `orcinus kubectl -n tetragon logs ds/tetragon -c tetragon`. Most often the kernel lacks BTF, or the node is under disk pressure and evicts the pod. |
| No events for a pod | Is it in `kube-system`? Those are not exported. Is it older than `--since`? Use `--since 0`. |
| A service crash-loops after `block-shell` | Its entrypoint is a shell script — see [Before you turn it on](#blocking-shells). Narrow the scope or `--off`. |
| Too many `write` events | An app writing many different files — often caches at startup (Python `.pyc`: set `PYTHONDONTWRITEBYTECODE=1`). Narrow with `-s`/`-n`, or filter the JSON downstream. |

## Turning it off

```bash
orcinus monitor block-shell --off   # stop blocking, keep watching
orcinus monitor disable             # remove the agents, policies and the tetragon namespace
```

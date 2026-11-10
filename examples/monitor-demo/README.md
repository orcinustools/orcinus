# monitor-demo — watch and stop a remote code execution

A **deliberately vulnerable** service: `GET /?cmd=<command>` runs `<command>`
through `os.system` (a shell). It shows what `orcinus monitor` records when an
attacker gets code execution, and how `block-shell` stops it.
Do not expose it outside the cluster.

```bash
orcinus kubectl create namespace monitor-demo
orcinus deploy -n monitor-demo
orcinus monitor enable

# Play the attacker from a throwaway pod:
POD_IP=$(orcinus kubectl -n monitor-demo get pod -l io.kompose.service=rce -o jsonpath='{.items[0].status.podIP}')
attack() { orcinus kubectl -n monitor-demo run attacker --rm -i --restart=Never --image=alpine:3.20 -- wget -qO- "http://$POD_IP:8080/?cmd=$1"; }

attack 'id>/tmp/pwned;chmod+777+/tmp/pwned'    # rc=0
orcinus monitor -n monitor-demo -s rce --since 5m
```

```
… exec     uid=0  /bin/sh -c "id>/tmp/pwned;chmod 777 /tmp/pwned"
… exec     uid=0  /usr/bin/id
… write    uid=0  /tmp/pwned  by /usr/bin/id
… exec     uid=0  /bin/chmod 777 /tmp/pwned
… chmod    uid=0  /tmp/pwned → 0777  by /bin/chmod
```

Now block shells for this project and attack again:

```bash
orcinus monitor block-shell --project monitor-demo
attack id                                         # rc=9 — the shell was killed
orcinus monitor exec -n monitor-demo --since 1m   # blocked  /bin/sh  by /usr/local/bin/python
orcinus kubectl -n monitor-demo get pods          # rce still Running, no restarts
```

Clean up:

```bash
orcinus monitor block-shell --off
orcinus rm monitor-demo -n monitor-demo
```

The project name comes from the folder (`monitor-demo`); pass `--project` to
`orcinus deploy` to change it. See [docs/MONITORING.md](../../docs/MONITORING.md).

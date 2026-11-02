package cluster

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// upgradePath is the runtime release orcinus steps through for each Kubernetes
// minor: a control plane cannot skip minors, so a 1.31 cluster goes 1.32, 1.33,
// … in turn. The last entry is the version new clusters get (DefaultImage and
// the Makefile's RUNTIME_VERSION); bump all three together.
var upgradePath = []string{
	"v1.32.13+k3s1",
	"v1.33.13+k3s2",
	"v1.34.12+k3s1",
	"v1.35.9+k3s1",
	"v1.36.5+k3s1",
}

// TargetVersion is the runtime version `cluster update --upgrade` ends on.
func TargetVersion() string { return upgradePath[len(upgradePath)-1] }

func minorPatch(v string) (minor, patch int, ok bool) {
	_, err := fmt.Sscanf(v, "v1.%d.%d", &minor, &patch)
	return minor, patch, err == nil
}

// upgradeSteps returns the versions to go through, in order, to get from cur
// to TargetVersion (empty when already there).
func upgradeSteps(cur string) ([]string, error) {
	cm, cp, ok := minorPatch(cur)
	if !ok {
		return nil, fmt.Errorf("cannot tell the runtime version from %q", cur)
	}
	if first, _, _ := minorPatch(upgradePath[0]); cm < first-1 {
		return nil, fmt.Errorf("upgrading from 1.%d is not supported (orcinus steps from 1.%d)", cm, first-1)
	}
	var steps []string
	for _, v := range upgradePath {
		if m, p, _ := minorPatch(v); m > cm || (m == cm && p > cp) {
			steps = append(steps, v)
		}
	}
	return steps, nil
}

// Image tags spell the version with "-" where the release has "+".
func dockerTag(v string) string     { return strings.Replace(v, "+", "-", 1) }
func fromDockerTag(t string) string { return strings.Replace(t, "-k3s", "+k3s", 1) }

// Upgrade moves a cluster node to TargetVersion. A control-plane node steps one
// minor at a time, waiting for the node to report each version; a worker jumps
// straight to the target (workers may lag, but should not stay behind). With the
// docker runtime the other nodes of the cluster on this host follow the server.
// It returns the version the node ended on.
func Upgrade(name string) (string, error) {
	st, stErr := LoadState()
	if name == "" {
		name = DefaultName
		if stErr == nil {
			name = st.Name
		}
	}
	if stErr == nil && st.Runtime == "standalone" {
		return upgradeStandalone(name)
	}
	if exists, running := containerState(name); !exists || !running {
		return "", fmt.Errorf("cluster node %q is not running", name)
	}
	if err := upgradeNode(name, true); err != nil {
		return "", err
	}
	if stErr == nil && st.Name == name {
		st.Image = "rancher/k3s:" + dockerTag(TargetVersion())
		_ = saveState(st)
		// The workers and extra servers on this host.
		ids, _ := docker("ps", "--format", "{{.Names}}", "--filter", "label=orcinus.cluster="+name)
		for _, n := range strings.Fields(ids) {
			if n != name {
				fmt.Fprintf(os.Stderr, "upgrading node %s...\n", n)
				if err := upgradeNode(n, false); err != nil {
					return "", fmt.Errorf("node %s: %w", n, err)
				}
			}
		}
	}
	return TargetVersion(), nil
}

// upgradeNode recreates one node container on newer images. step says whether a
// server walks every minor (the first server of the cluster) or jumps to the
// target (workers, and servers that follow an already-upgraded one).
func upgradeNode(name string, step bool) error {
	c, err := inspectNode(name)
	if err != nil {
		return err
	}
	repo, tag, _ := strings.Cut(c.Config.Image, ":")
	steps, err := upgradeSteps(fromDockerTag(tag))
	if err != nil {
		return err
	}
	if len(steps) == 0 {
		return nil
	}
	server := len(c.Config.Cmd) > 0 && c.Config.Cmd[0] == "server"
	if server && step {
		// ponytail: one control plane only; HA needs every server per minor in lockstep.
		out, err := docker("exec", name, "kubectl", "get", "nodes", "--no-headers", "-l", "node-role.kubernetes.io/control-plane")
		if err != nil {
			return fmt.Errorf("list control-plane nodes: %w\n%s", err, out)
		}
		if n := len(strings.Split(strings.TrimSpace(out), "\n")); n > 1 {
			return fmt.Errorf("the cluster has %d control-plane nodes; --upgrade handles single-server clusters only", n)
		}
	}
	if !step {
		steps = steps[len(steps)-1:]
	}
	gpu := repo == "orcinus/k3s-nvidia"
	if gpu {
		repo = "rancher/k3s"
	}
	for _, v := range steps {
		img := repo + ":" + dockerTag(v)
		fmt.Fprintf(os.Stderr, "%s: %s → %s\n", name, c.Config.Image, img)
		// Fetch first: the node is down only from here on.
		if gpu {
			if img, err = gpuImage(img); err != nil {
				return err
			}
		} else if out, err := docker("pull", img); err != nil {
			return fmt.Errorf("pull %s: %w\n%s", img, err, out)
		}
		c.Config.Image = img
		args := c.createArgs(name, c.publishedPort("80"), c.publishedPort("443"))
		ready := func() error { return waitRunning(name, 20*time.Second) }
		if server {
			ready = func() error { return waitNodeVersion(name, c.Config.Hostname, v, 5*time.Minute) }
		}
		if err := recreate(name, c, args, ready); err != nil {
			return err
		}
	}
	return nil
}

// waitNodeVersion waits until the server reports node Ready on version v.
func waitNodeVersion(name, node, v string, timeout time.Duration) error {
	want := v + " True"
	var got string
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		got, _ = docker("exec", name, "kubectl", "get", "node", node, "-o",
			`jsonpath={.status.nodeInfo.kubeletVersion} {.status.conditions[?(@.type=="Ready")].status}`)
		if strings.TrimSpace(got) == want {
			return nil
		}
	}
	return fmt.Errorf("node %s did not come up Ready on %s within %s (last: %q)", node, v, timeout, strings.TrimSpace(got))
}

// waitRunning checks a node that has no API of its own (a worker) stays up.
func waitRunning(name string, d time.Duration) error {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(2 * time.Second) {
		if _, running := containerState(name); !running {
			out, _ := docker("logs", "--tail", "20", name)
			return fmt.Errorf("node %s exited after the upgrade:\n%s", name, out)
		}
	}
	return nil
}

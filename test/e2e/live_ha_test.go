package e2e

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveHACluster brings up an HA-style topology on one Docker host: a first
// control-plane node with embedded etcd, a second control-plane node (join
// --role server), and a worker (join --role agent) — then asserts 3 nodes with
// 2 control-planes. Skipped unless ORCINUS_E2E_LIVE is set.
func TestLiveHACluster(t *testing.T) {
	if os.Getenv("ORCINUS_E2E_LIVE") == "" {
		t.Skip("set ORCINUS_E2E_LIVE=1 to run the live HA cluster e2e")
	}

	docker := strings.Fields(envOr("ORCINUS_E2E_DOCKER", "docker"))
	const name = "orcinus-ha"
	names := []string{name, name + "-2", name + "-w1"}

	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "ORCINUS_DOCKER="+strings.Join(docker, " "))
	orcinus := func(args ...string) (string, error) {
		cmd := exec.Command(orcinusBin, args...)
		cmd.Dir = repoRoot()
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	rmAll := func() {
		for _, n := range names {
			_, _ = runcOut(append(docker, "rm", "-f", n)...)
		}
	}
	rmAll()
	t.Cleanup(rmAll)

	// --- first control-plane node with embedded etcd (HA datastore) ---
	t.Log("cluster init --cluster-init (first master)")
	if out, err := orcinus("cluster", "init", "--name", name, "--port", "16445", "--cluster-init"); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}

	// --- second control-plane node ---
	t.Log("cluster join --role server (second master)")
	if out, err := orcinus("cluster", "join", "--role", "server", "--name", name+"-2"); err != nil {
		t.Fatalf("server join failed: %v\n%s", err, out)
	}

	// --- worker node ---
	t.Log("cluster join --role agent (worker)")
	if out, err := orcinus("cluster", "join", "--role", "agent", "--name", name+"-w1"); err != nil {
		t.Fatalf("agent join failed: %v\n%s", err, out)
	}

	kubectl := func(args ...string) (string, error) {
		return runcOut(append(append(docker, "exec", name, "kubectl"), args...)...)
	}
	waitFor(t, 300*time.Second, "3 nodes Ready", func() bool {
		out, err := kubectl("get", "nodes", "--no-headers")
		return err == nil && readyNodeCount(out) >= 3
	})

	out, err := kubectl("get", "nodes", "--no-headers")
	if err != nil {
		t.Fatalf("get nodes: %v\n%s", err, out)
	}
	if cp := controlPlaneCount(out); cp < 2 {
		t.Fatalf("expected >=2 control-plane nodes, got %d:\n%s", cp, out)
	}

	if out, err := orcinus("cluster", "down"); err != nil {
		t.Fatalf("cluster down failed: %v\n%s", err, out)
	}
	t.Log("live HA e2e passed: init(etcd) + join server + join agent → 3 nodes, 2 control-plane")
}

// controlPlaneCount counts nodes whose ROLES column includes control-plane.
func controlPlaneCount(nodesOutput string) int {
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(nodesOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.Contains(fields[2], "control-plane") {
			n++
		}
	}
	return n
}

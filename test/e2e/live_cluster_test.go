package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveCluster exercises M3 end to end through the orcinus binary: `orcinus
// init` brings up a cluster, `orcinus join` adds a second node, then deploy/ls/ps/
// rm all work with NO --kubeconfig (they use ~/.orcinus/kubeconfig written by
// init, via a temp HOME). Skipped unless ORCINUS_E2E_LIVE is set.
func TestLiveCluster(t *testing.T) {
	if os.Getenv("ORCINUS_E2E_LIVE") == "" {
		t.Skip("set ORCINUS_E2E_LIVE=1 to run the live cluster e2e")
	}

	docker := strings.Fields(envOr("ORCINUS_E2E_DOCKER", "docker"))
	const name = "orcinus-m3"
	const project = "m3"

	// Isolated HOME so orcinus writes ~/.orcinus here, and its cluster runtime
	// uses the same docker command as the test.
	home := t.TempDir()
	env := append(os.Environ(),
		"HOME="+home,
		"ORCINUS_DOCKER="+strings.Join(docker, " "),
	)
	orcinus := func(args ...string) (string, error) {
		cmd := exec.Command(orcinusBin, args...)
		cmd.Dir = repoRoot()
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	_, _ = runcOut(append(docker, "rm", "-f", name)...)
	_, _ = runcOut(append(docker, "rm", "-f", name+"-agent")...)
	t.Cleanup(func() {
		_, _ = runcOut(append(docker, "rm", "-f", name)...)
		_, _ = runcOut(append(docker, "rm", "-f", name+"-agent")...)
	})

	kubeconfigPath := filepath.Join(home, ".orcinus", "kubeconfig")

	// --- 1. orcinus init ---
	t.Log("orcinus init")
	if out, err := orcinus("cluster", "init", "--name", name, "--port", "16444"); err != nil {
		t.Fatalf("orcinus init failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(kubeconfigPath); err != nil {
		t.Fatalf("init did not write ~/.orcinus/kubeconfig: %v", err)
	}

	// --- 1b. init is idempotent: a second init reuses the running cluster ---
	if out, err := orcinus("cluster", "init", "--name", name, "--port", "16444"); err != nil {
		t.Fatalf("second (idempotent) init failed: %v\n%s", err, out)
	}

	// --- 2. orcinus join (second node, reads saved state) ---
	t.Log("orcinus join")
	if out, err := orcinus("cluster", "join", "--name", name+"-agent"); err != nil {
		t.Fatalf("orcinus join failed: %v\n%s", err, out)
	}
	kubectl := func(args ...string) (string, error) {
		return runcOut(append(append(docker, "exec", name, "kubectl"), args...)...)
	}
	waitFor(t, 180*time.Second, "2 nodes Ready", func() bool {
		out, err := kubectl("get", "nodes", "--no-headers")
		return err == nil && readyNodeCount(out) >= 2
	})

	// --- 2b. orcinus status shows a running cluster with both nodes ---
	if out, err := orcinus("cluster", "status"); err != nil {
		t.Fatalf("orcinus status failed: %v\n%s", err, out)
	} else if !strings.Contains(out, "running") || readyNodeCount(out) < 2 {
		t.Fatalf("orcinus status did not report 2 running nodes:\n%s", out)
	}

	// --- 3. orcinus deploy with NO --kubeconfig (uses ~/.orcinus/kubeconfig) ---
	t.Log("orcinus deploy (auto kubeconfig)")
	if out, err := orcinus("deploy", "-f", composeFixture(),
		"--project", project, "--namespace", "default", "--wait"); err != nil {
		t.Fatalf("orcinus deploy failed: %v\n%s", err, out)
	}

	// --- 4. ls / ps with no --kubeconfig ---
	if out, err := orcinus("ls"); err != nil || !strings.Contains(out, project) {
		t.Fatalf("orcinus ls did not show %q: err=%v\n%s", project, err, out)
	}
	if out, err := orcinus("ps", project); err != nil || !strings.Contains(out, "web") {
		t.Fatalf("orcinus ps did not show web: err=%v\n%s", err, out)
	}

	// --- 5. orcinus rm ---
	if out, err := orcinus("rm", project); err != nil {
		t.Fatalf("orcinus rm failed: %v\n%s", err, out)
	}

	// --- 6. orcinus down tears the whole cluster down ---
	t.Log("orcinus down")
	if out, err := orcinus("cluster", "down"); err != nil {
		t.Fatalf("orcinus down failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(kubeconfigPath); !os.IsNotExist(err) {
		t.Errorf("down should have removed the kubeconfig, stat err = %v", err)
	}
	if out, _ := runcOut(append(docker, "ps", "-aq", "--filter", "label=orcinus.cluster="+name)...); strings.TrimSpace(out) != "" {
		t.Errorf("down should have removed all cluster containers, still present:\n%s", out)
	}

	t.Log("live cluster e2e passed: init(idempotent) → join(2 nodes) → status → deploy → ls → ps → rm → down")
}

// readyNodeCount counts nodes whose STATUS column is exactly "Ready".
func readyNodeCount(nodesOutput string) int {
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(nodesOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "Ready" {
			n++
		}
	}
	return n
}

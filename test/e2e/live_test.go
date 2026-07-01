package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveSingleNode boots a real single-node k3s cluster in Docker, converts the
// example compose file with orcinus, applies the manifests, and asserts the
// workloads actually come up. It is skipped unless ORCINUS_E2E_LIVE is set.
//
// Env knobs:
//
//	ORCINUS_E2E_LIVE   set to any value to enable the test
//	ORCINUS_E2E_DOCKER docker command (default "docker"; e.g. "sudo docker")
//	ORCINUS_E2E_K3S_IMAGE  k3s image (default rancher/k3s:v1.31.5-k3s1)
func TestLiveSingleNode(t *testing.T) {
	if os.Getenv("ORCINUS_E2E_LIVE") == "" {
		t.Skip("set ORCINUS_E2E_LIVE=1 to run the live single-node k3s e2e")
	}

	docker := dockerCmd()
	image := envOr("ORCINUS_E2E_K3S_IMAGE", "rancher/k3s:v1.31.5-k3s1")
	const name = "orcinus-e2e-k3s"
	const ns = "orcinus-e2e"

	// Clean any leftover container, and always clean up afterward.
	_ = run(docker, "rm", "-f", name)
	t.Cleanup(func() { _ = run(docker, "rm", "-f", name) })

	t.Logf("starting single-node k3s (%s)", image)
	if out, err := runOut(docker, "run", "-d", "--privileged",
		"--name", name,
		"-p", "127.0.0.1:16443:6443",
		image, "server",
		"--disable=traefik", "--disable=metrics-server", "--disable=servicelb",
		"--write-kubeconfig-mode=644",
	); err != nil {
		t.Fatalf("docker run k3s: %v\n%s", err, out)
	}

	// kubectl inside the container talks to the local k3s.
	kubectl := func(args ...string) (string, error) {
		full := append([]string{"exec", name, "kubectl"}, args...)
		return runOut(docker, full...)
	}

	waitFor(t, 180*time.Second, "k3s node Ready", func() bool {
		out, err := kubectl("get", "nodes", "--no-headers")
		return err == nil && strings.Contains(out, " Ready")
	})

	// Convert the example compose file with the orcinus binary. Capture stdout
	// ONLY — kompose emits warnings on stderr that would corrupt the manifest.
	manifests, err := stdoutOnly(orcinusBin, "deploy",
		"-f", composeFixture(),
		"--dry-run",
		"--namespace", ns,
		"--project", "e2e",
	)
	if err != nil {
		t.Fatalf("orcinus convert: %v\n%s", err, manifests)
	}
	if !strings.Contains(manifests, "kind: StatefulSet") {
		t.Fatalf("converted output missing StatefulSet:\n%s", manifests)
	}

	// Create the namespace and apply the converted manifests via the cluster's
	// own kubectl (piping the orcinus output into `kubectl apply -f -`).
	if out, err := kubectl("create", "namespace", ns); err != nil {
		t.Fatalf("create namespace: %v\n%s", err, out)
	}
	if out, err := applyStdin(docker, name, manifests); err != nil {
		t.Fatalf("kubectl apply: %v\n%s", err, out)
	}

	// The nginx Deployment should roll out.
	if out, err := kubectl("-n", ns, "rollout", "status", "deployment/web", "--timeout=180s"); err != nil {
		t.Fatalf("web deployment did not become available: %v\n%s", err, out)
	}

	// The postgres StatefulSet pod should become Ready (PVC bound via local-path).
	waitFor(t, 240*time.Second, "db statefulset ready", func() bool {
		out, err := kubectl("-n", ns, "get", "statefulset", "db",
			"-o", "jsonpath={.status.readyReplicas}")
		return err == nil && strings.TrimSpace(out) == "1"
	})

	// Sanity-check the other converted resources exist on the live cluster.
	for _, res := range []string{"service/web", "ingress/web", "pvc/dbdata", "secret/db-secret"} {
		if out, err := kubectl("-n", ns, "get", res); err != nil {
			t.Errorf("expected %s on cluster: %v\n%s", res, err, out)
		}
	}

	// The extracted secret must be consumable and hold the original value.
	out, err := kubectl("-n", ns, "get", "secret", "db-secret",
		"-o", "jsonpath={.data.POSTGRES_PASSWORD}")
	if err != nil {
		t.Fatalf("get secret: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Errorf("db-secret POSTGRES_PASSWORD is empty on cluster")
	}

	t.Log("live single-node e2e passed: compose → orcinus → running k3s workloads")
}

// --- helpers ---

func dockerCmd() []string {
	return strings.Fields(envOr("ORCINUS_E2E_DOCKER", "docker"))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// run executes a command (first element may itself be a multi-word prefix like
// "sudo docker") and discards output.
func run(prefix []string, args ...string) error {
	_, err := runOut(prefix, args...)
	return err
}

func runOut(prefix interface{}, args ...string) (string, error) {
	var argv []string
	switch p := prefix.(type) {
	case []string:
		argv = append(argv, p...)
	case string:
		argv = append(argv, p)
	default:
		return "", fmt.Errorf("bad prefix type %T", prefix)
	}
	argv = append(argv, args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = repoRoot()
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// stdoutOnly runs a command and returns only its stdout (stderr is discarded).
func stdoutOnly(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = repoRoot()
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	return string(out), err
}

// applyStdin pipes manifests into `<docker> exec -i <name> kubectl apply -f -`.
func applyStdin(docker []string, name, manifests string) (string, error) {
	argv := append([]string{}, docker...)
	argv = append(argv, "exec", "-i", name, "kubectl", "apply", "-f", "-")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(manifests)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}

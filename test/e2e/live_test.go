package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveSingleNode boots a real single-node cluster in Docker and drives the
// full M2 path through the orcinus binary itself: `orcinus deploy` (server-side
// apply + wait), prune on re-deploy, and `orcinus rm`. Skipped unless
// ORCINUS_E2E_LIVE is set.
//
// Env knobs:
//
//	ORCINUS_E2E_LIVE       set to enable the test
//	ORCINUS_E2E_DOCKER     docker command (default "docker"; e.g. "sudo docker")
//	ORCINUS_E2E_K3S_IMAGE  cluster image (default rancher/k3s:v1.31.5-k3s1)
func TestLiveSingleNode(t *testing.T) {
	if os.Getenv("ORCINUS_E2E_LIVE") == "" {
		t.Skip("set ORCINUS_E2E_LIVE=1 to run the live single-node e2e")
	}

	docker := strings.Fields(envOr("ORCINUS_E2E_DOCKER", "docker"))
	image := envOr("ORCINUS_E2E_K3S_IMAGE", "rancher/k3s:v1.31.5-k3s1")
	const name = "orcinus-e2e-node"
	const project = "e2e"

	_ = runc(append(docker, "rm", "-f", name)...)
	t.Cleanup(func() { _ = runc(append(docker, "rm", "-f", name)...) })

	t.Logf("starting single-node cluster (%s)", image)
	if out, err := runcOut(append(docker, "run", "-d", "--privileged",
		"--name", name,
		"-p", "127.0.0.1:16443:6443",
		image, "server",
		"--disable=traefik", "--disable=metrics-server", "--disable=servicelb",
		"--write-kubeconfig-mode=644",
	)...); err != nil {
		t.Fatalf("start cluster: %v\n%s", err, out)
	}

	kubectl := func(args ...string) (string, error) {
		return runcOut(append(append(docker, "exec", name, "kubectl"), args...)...)
	}

	waitFor(t, 180*time.Second, "node Ready", func() bool {
		out, err := kubectl("get", "nodes", "--no-headers")
		return err == nil && strings.Contains(out, " Ready")
	})

	// Pull the kubeconfig out of the container and point it at the mapped port,
	// so the orcinus binary on the host can reach the cluster.
	kubeconfig := extractKubeconfig(t, docker, name)

	orcinus := func(args ...string) (string, error) {
		cmd := exec.Command(orcinusBin, args...)
		cmd.Dir = repoRoot()
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// --- 1. orcinus deploy (apply + wait) ---
	t.Log("orcinus deploy --wait")
	if out, err := orcinus("deploy",
		"-f", composeFixture(),
		"--kubeconfig", kubeconfig,
		"--namespace", "default",
		"--project", project,
		"--wait",
	); err != nil {
		t.Fatalf("orcinus deploy failed: %v\n%s", err, out)
	}

	// Everything the compose file implies must now exist on the cluster.
	for _, res := range []string{
		"deployment/web", "statefulset/db", "service/web",
		"ingress/web", "pvc/dbdata", "secret/db-secret",
	} {
		if out, err := kubectl("-n", "default", "get", res); err != nil {
			t.Fatalf("expected %s after deploy: %v\n%s", res, err, out)
		}
	}

	// --- 1b. orcinus ls must show the project ---
	if out, err := orcinus("ls", "--kubeconfig", kubeconfig); err != nil {
		t.Fatalf("orcinus ls failed: %v\n%s", err, out)
	} else if !strings.Contains(out, project) {
		t.Fatalf("orcinus ls did not list project %q:\n%s", project, out)
	}

	// --- 1c. orcinus ps must list the project's pods ---
	if out, err := orcinus("ps", project, "--kubeconfig", kubeconfig); err != nil {
		t.Fatalf("orcinus ps failed: %v\n%s", err, out)
	} else if !strings.Contains(out, "web") || !strings.Contains(out, "db") {
		t.Fatalf("orcinus ps missing services web/db:\n%s", out)
	}

	// --- 1d. orcinus logs must stream a service's logs ---
	if out, err := orcinus("logs", "web", "--project", project, "--kubeconfig", kubeconfig, "-n", "default"); err != nil {
		t.Fatalf("orcinus logs failed: %v\n%s", err, out)
	}

	// --- 2. idempotency: re-deploy must succeed ---
	if out, err := orcinus("deploy", "-f", composeFixture(),
		"--kubeconfig", kubeconfig, "--namespace", "default", "--project", project); err != nil {
		t.Fatalf("re-deploy (idempotency) failed: %v\n%s", err, out)
	}

	// --- 3. prune: deploy a web-only compose, db resources must be pruned ---
	webOnly := writeTemp(t, "web-only.yml", `
services:
  web:
    image: nginx:1.27
    ports: ["80:80"]
    x-orcinus-expose: ingress
    x-orcinus-host: web.local
`)
	t.Log("orcinus deploy (web-only) → expect db pruned")
	if out, err := orcinus("deploy", "-f", webOnly,
		"--kubeconfig", kubeconfig, "--namespace", "default", "--project", project); err != nil {
		t.Fatalf("prune deploy failed: %v\n%s", err, out)
	}
	waitFor(t, 60*time.Second, "db statefulset pruned", func() bool {
		_, err := kubectl("-n", "default", "get", "statefulset", "db")
		return err != nil // NotFound
	})
	if _, err := kubectl("-n", "default", "get", "secret", "db-secret"); err == nil {
		t.Errorf("db-secret should have been pruned")
	}
	// web must survive the prune.
	if out, err := kubectl("-n", "default", "get", "deployment", "web"); err != nil {
		t.Fatalf("web must survive prune: %v\n%s", err, out)
	}

	// --- 4. orcinus rm: removes the whole project ---
	t.Log("orcinus rm")
	if out, err := orcinus("rm", project, "--kubeconfig", kubeconfig, "--namespace", "default"); err != nil {
		t.Fatalf("orcinus rm failed: %v\n%s", err, out)
	}
	waitFor(t, 60*time.Second, "web deployment removed", func() bool {
		_, err := kubectl("-n", "default", "get", "deployment", "web")
		return err != nil // NotFound
	})

	t.Log("live single-node e2e passed: deploy → idempotent re-deploy → prune → rm")
}

// --- helpers ---

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runc(argv ...string) error {
	_, err := runcOut(argv...)
	return err
}

func runcOut(argv ...string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = repoRoot()
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func extractKubeconfig(t *testing.T, docker []string, name string) string {
	t.Helper()
	raw, err := runcOut(append(docker, "exec", name, "cat", "/etc/rancher/k3s/k3s.yaml")...)
	if err != nil {
		t.Fatalf("read kubeconfig: %v\n%s", err, raw)
	}
	raw = strings.ReplaceAll(raw, "127.0.0.1:6443", "127.0.0.1:16443")
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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

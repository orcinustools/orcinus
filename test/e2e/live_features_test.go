package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// liveCluster boots a single-node cluster for a feature test and returns helpers
// to run the orcinus binary (with an isolated HOME) and kubectl inside it.
// It registers cleanup (cluster down + container removal). Skips are the caller's
// responsibility.
func liveCluster(t *testing.T, name string, port int, initArgs ...string) (orcinus, kubectl func(args ...string) (string, error)) {
	t.Helper()
	docker := strings.Fields(envOr("ORCINUS_E2E_DOCKER", "docker"))
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "ORCINUS_DOCKER="+strings.Join(docker, " "))

	orcinus = func(args ...string) (string, error) {
		cmd := exec.Command(orcinusBin, args...)
		cmd.Dir = repoRoot()
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	kubectl = func(args ...string) (string, error) {
		return runcOut(append(append(docker, "exec", name, "kubectl"), args...)...)
	}

	_, _ = runcOut(append(docker, "rm", "-f", name)...)
	t.Cleanup(func() {
		_, _ = orcinus("cluster", "down")
		_, _ = runcOut(append(docker, "rm", "-f", name)...)
	})

	args := append([]string{"cluster", "init", "--name", name, "--port", fmt.Sprintf("%d", port)}, initArgs...)
	if out, err := orcinus(args...); err != nil {
		t.Fatalf("cluster init: %v\n%s", err, out)
	}
	return orcinus, kubectl
}

func requireLive(t *testing.T) {
	if os.Getenv("ORCINUS_E2E_LIVE") == "" {
		t.Skip("set ORCINUS_E2E_LIVE=1 to run live e2e")
	}
}

func writeCompose(t *testing.T, content string) string {
	t.Helper()
	return writeTemp(t, "orcinus.yml", content)
}

// TestLiveStrategy: recreate + Swarm update_config map to the Deployment strategy.
func TestLiveStrategy(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-strat", 16471)
	f := writeCompose(t, `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-strategy: recreate
  api:
    image: nginx:1.27
    ports: ["8080"]
    deploy:
      update_config: { order: start-first, parallelism: 2 }
`)
	if out, err := orcinus("deploy", "-f", f, "--project", "s", "--wait"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if got, _ := kubectl("get", "deploy", "web", "-o", "jsonpath={.spec.strategy.type}"); got != "Recreate" {
		t.Errorf("web strategy = %q, want Recreate", got)
	}
	if got, _ := kubectl("get", "deploy", "api", "-o", "jsonpath={.spec.strategy.rollingUpdate.maxSurge}"); got != "2" {
		t.Errorf("api maxSurge = %q, want 2", got)
	}
}

// TestLiveAutoscale: x-orcinus-autoscale → HPA that gets live metrics; scale/autoscale.
func TestLiveAutoscale(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-as", 16472)
	f := writeCompose(t, `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    deploy:
      resources:
        limits: { cpus: "0.2", memory: 128M }
        reservations: { cpus: "0.05", memory: 64M }
    x-orcinus-autoscale-min: 2
    x-orcinus-autoscale-max: 5
    x-orcinus-autoscale-cpu: 70
`)
	if out, err := orcinus("deploy", "-f", f, "--project", "a"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	// HPA exists with correct bounds.
	if got, _ := kubectl("get", "hpa", "web", "-o", "jsonpath={.spec.maxReplicas}"); got != "5" {
		t.Errorf("hpa maxReplicas = %q, want 5", got)
	}
	// Metrics must become active (cluster init auto-enables metrics-server).
	waitFor(t, 150*time.Second, "HPA metrics active", func() bool {
		line, _ := kubectl("get", "hpa", "web", "--no-headers")
		return strings.Contains(line, "cpu: ") && !strings.Contains(line, "<unknown>")
	})
	// scale + autoscale commands.
	if out, err := orcinus("scale", "web", "3"); err != nil {
		t.Fatalf("scale: %v\n%s", err, out)
	}
	if out, err := orcinus("autoscale", "web", "--min", "1", "--max", "4", "--cpu", "50"); err != nil {
		t.Fatalf("autoscale: %v\n%s", err, out)
	}
	if got, _ := kubectl("get", "hpa", "web", "-o", "jsonpath={.spec.maxReplicas}"); got != "4" {
		t.Errorf("hpa maxReplicas after autoscale = %q, want 4", got)
	}
}

// TestLiveRollout: canary + blue-green auto-install Argo Rollouts and go Healthy.
func TestLiveRollout(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-ro", 16473)
	f := writeCompose(t, `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-rollout: canary
  api:
    image: nginx:1.27
    ports: ["8080"]
    x-orcinus-rollout: bluegreen
`)
	if out, err := orcinus("deploy", "-f", f, "--project", "r"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !pluginInstalled(orcinus, "argo-rollouts") {
		t.Errorf("argo-rollouts should be auto-installed")
	}
	waitFor(t, 240*time.Second, "both rollouts Healthy", func() bool {
		w, _ := kubectl("get", "rollout", "web", "-o", "jsonpath={.status.phase}")
		a, _ := kubectl("get", "rollout", "api", "-o", "jsonpath={.status.phase}")
		return w == "Healthy" && a == "Healthy"
	})
}

// TestLivePlugins: install/remove an inline plugin, with namespace cleanup.
func TestLivePlugins(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-pl", 16474)
	if out, err := orcinus("plugin", "install", "registry"); err != nil {
		t.Fatalf("install registry: %v\n%s", err, out)
	}
	if out, err := kubectl("-n", "orcinus-registry", "get", "deploy", "registry"); err != nil {
		t.Fatalf("registry deploy missing: %v\n%s", err, out)
	}
	if out, err := orcinus("plugin", "remove", "registry"); err != nil {
		t.Fatalf("remove registry: %v\n%s", err, out)
	}
	waitFor(t, 60*time.Second, "registry namespace removed", func() bool {
		_, err := kubectl("get", "ns", "orcinus-registry")
		return err != nil
	})
}

// TestLiveStorageMinIO: distributed (HA) object storage StatefulSet comes up.
func TestLiveStorageMinIO(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-st", 16475)
	if out, err := orcinus("plugin", "install", "storage", "--provider", "minio", "--replicas", "4", "--size", "1Gi"); err != nil {
		t.Fatalf("install minio: %v\n%s", err, out)
	}
	waitFor(t, 240*time.Second, "minio statefulset 4/4", func() bool {
		r, _ := kubectl("-n", "orcinus-storage", "get", "statefulset", "minio", "-o", "jsonpath={.status.readyReplicas}")
		return strings.TrimSpace(r) == "4"
	})
	if out, err := orcinus("plugin", "remove", "storage", "--provider", "minio", "--replicas", "4"); err != nil {
		t.Fatalf("remove minio: %v\n%s", err, out)
	}
}

func pluginInstalled(orcinus func(args ...string) (string, error), name string) bool {
	out, _ := orcinus("plugin", "list")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == name && f[1] == "yes" {
			return true
		}
	}
	return false
}

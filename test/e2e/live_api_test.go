package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveAPI boots a cluster, starts `orcinus api` against it, and drives the
// full workflow over HTTP: auth, deploy, list projects/pods, scale, secrets, and
// project removal.
func TestLiveAPI(t *testing.T) {
	requireLive(t)
	docker := strings.Fields(envOr("ORCINUS_E2E_DOCKER", "docker"))
	home := t.TempDir()
	env := append(os.Environ(), "HOME="+home, "ORCINUS_DOCKER="+strings.Join(docker, " "))
	const name = "orcinus-api"
	const apiAddr = "127.0.0.1:18099"
	const token = "testtoken"
	kubeconfig := home + "/.orcinus/kubeconfig"

	orc := func(args ...string) (string, error) {
		cmd := exec.Command(orcinusBin, args...)
		cmd.Dir = repoRoot()
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	_, _ = runcOut(append(docker, "rm", "-f", name)...)
	t.Cleanup(func() {
		_, _ = orc("cluster", "down")
		_, _ = runcOut(append(docker, "rm", "-f", name)...)
	})
	if out, err := orc("cluster", "init", "--name", name, "--port", "16485"); err != nil {
		t.Fatalf("cluster init: %v\n%s", err, out)
	}

	// Start the API server as a background process against this cluster.
	apiCmd := exec.Command(orcinusBin, "api", "--addr", apiAddr, "--token", token, "--kubeconfig", kubeconfig)
	apiCmd.Env = env
	apiCmd.Stdout, apiCmd.Stderr = os.Stderr, os.Stderr
	if err := apiCmd.Start(); err != nil {
		t.Fatalf("start api: %v", err)
	}
	t.Cleanup(func() { _ = apiCmd.Process.Kill() })

	base := "http://" + apiAddr
	curl := func(method, path, body string, authed bool) (int, string) {
		args := []string{"curl", "-s", "-o", "/dev/stdout", "-w", "\n%{http_code}", "-X", method, "-m", "60"}
		if authed {
			args = append(args, "-H", "Authorization: Bearer "+token)
		}
		if body != "" {
			args = append(args, "-H", "Content-Type: application/json", "-d", body)
		}
		args = append(args, base+path)
		out, _ := runcOut(args...)
		i := strings.LastIndex(out, "\n")
		if i < 0 {
			return 0, out
		}
		code := 0
		fmt.Sscanf(strings.TrimSpace(out[i+1:]), "%d", &code)
		return code, out[:i]
	}

	// Wait for the server to be up.
	waitFor(t, 30*time.Second, "api /healthz up", func() bool {
		code, _ := curl("GET", "/healthz", "", false)
		return code == 200
	})

	// Auth: /api/v1/* requires the token.
	if code, _ := curl("GET", "/api/v1/plugins", "", false); code != 401 {
		t.Errorf("unauthenticated plugins = %d, want 401", code)
	}
	if code, body := curl("GET", "/api/v1/plugins", "", true); code != 200 || !strings.Contains(body, "cert-manager") {
		t.Errorf("authenticated plugins = %d\n%s", code, body)
	}

	// Deploy an app via the API (JSON body with an embedded compose source).
	deployBody, _ := json.Marshal(map[string]interface{}{
		"source":  "services:\n  web:\n    image: nginx:1.27\n    ports: [\"80\"]\n",
		"project": "apitest",
		"wait":    true,
	})
	if code, body := curl("POST", "/api/v1/deploy", string(deployBody), true); code != 200 || !strings.Contains(body, `"applied"`) {
		t.Fatalf("deploy = %d\n%s", code, body)
	}

	// Projects list includes the deployed project.
	if code, body := curl("GET", "/api/v1/projects", "", true); code != 200 || !strings.Contains(body, "apitest") {
		t.Fatalf("projects = %d\n%s", code, body)
	}
	// Pods list for the project.
	if code, body := curl("GET", "/api/v1/projects/apitest/pods", "", true); code != 200 || !strings.Contains(body, "web") {
		t.Fatalf("pods = %d\n%s", code, body)
	}

	// Scale the service to 3 via the API, then confirm on the cluster.
	if code, body := curl("POST", "/api/v1/projects/apitest/services/web/scale", `{"replicas":3}`, true); code != 200 {
		t.Fatalf("scale = %d\n%s", code, body)
	}
	waitFor(t, 60*time.Second, "web scaled to 3", func() bool {
		out, _ := runcOut(append(docker, "exec", name, "kubectl", "get", "deploy", "web",
			"-o", "jsonpath={.spec.replicas}")...)
		return strings.TrimSpace(out) == "3"
	})

	// Secret create + list + delete.
	if code, body := curl("POST", "/api/v1/secrets", `{"name":"api-secret","data":{"FOO":"bar"}}`, true); code != 201 {
		t.Fatalf("create secret = %d\n%s", code, body)
	}
	if code, body := curl("GET", "/api/v1/secrets", "", true); code != 200 || !strings.Contains(body, "api-secret") {
		t.Fatalf("list secrets = %d\n%s", code, body)
	}
	if code, _ := curl("DELETE", "/api/v1/secrets/api-secret", "", true); code != 204 {
		t.Errorf("delete secret = %d, want 204", code)
	}

	// Cluster status.
	if code, body := curl("GET", "/api/v1/cluster", "", true); code != 200 || !strings.Contains(body, name) {
		t.Errorf("cluster status = %d\n%s", code, body)
	}

	// Remove the project.
	if code, body := curl("DELETE", "/api/v1/projects/apitest", "", true); code != 200 || !strings.Contains(body, `"removed"`) {
		t.Fatalf("remove project = %d\n%s", code, body)
	}
}

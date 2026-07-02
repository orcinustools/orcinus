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

// TestLiveRollback: rollback + kubectl passthrough + `ls` readiness column.
func TestLiveRollback(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-rb", 16483)

	deploy := func(image string) {
		f := writeCompose(t, "services:\n  web:\n    image: "+image+"\n    ports: [\"80\"]\n")
		if out, err := orcinus("deploy", "-f", f, "--project", "rb", "--wait"); err != nil {
			t.Fatalf("deploy %s: %v\n%s", image, err, out)
		}
	}
	image := func() string {
		out, _ := kubectl("get", "deploy", "web", "-o", "jsonpath={.spec.template.spec.containers[0].image}")
		return strings.TrimSpace(out)
	}

	deploy("nginx:1.27")

	// `orcinus ls` READY column.
	if out, _ := orcinus("ls"); !strings.Contains(out, "1/1") {
		t.Errorf("ls should show READY 1/1:\n%s", out)
	}
	// `orcinus kubectl` passthrough (falls back to the cluster's kubectl).
	if out, err := orcinus("kubectl", "get", "nodes", "--no-headers"); err != nil || !strings.Contains(out, "Ready") {
		t.Fatalf("kubectl passthrough failed: %v\n%s", err, out)
	}

	deploy("nginx:1.28")
	if got := image(); got != "nginx:1.28" {
		t.Fatalf("after update image = %q, want nginx:1.28", got)
	}
	if out, err := orcinus("rollback", "web"); err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	waitFor(t, 60*time.Second, "rolled back to nginx:1.27", func() bool { return image() == "nginx:1.27" })
}

// TestLivePlugins: install/upgrade/remove an inline plugin, with namespace cleanup.
func TestLivePlugins(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-pl", 16474)
	if out, err := orcinus("plugin", "install", "registry"); err != nil {
		t.Fatalf("install registry: %v\n%s", err, out)
	}
	if out, err := kubectl("-n", "orcinus-registry", "get", "deploy", "registry"); err != nil {
		t.Fatalf("registry deploy missing: %v\n%s", err, out)
	}
	if out, err := orcinus("plugin", "upgrade", "registry"); err != nil {
		t.Fatalf("upgrade registry: %v\n%s", err, out)
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

// TestLiveIngressTLS proves the full HTTPS path end to end: cert-manager + Traefik
// + an ACME HTTP-01 challenge on a real public domain. Requires ORCINUS_E2E_DOMAIN
// to resolve to this host with inbound 80/443 open. Uses Let's Encrypt STAGING by
// default (repeatable, no prod rate limits); set ORCINUS_E2E_ACME_PROD=1 for a
// trusted cert.
func TestLiveIngressTLS(t *testing.T) {
	requireLive(t)
	domain := os.Getenv("ORCINUS_E2E_DOMAIN")
	if domain == "" {
		t.Skip("set ORCINUS_E2E_DOMAIN=<public host → this machine, ports 80/443 open>")
	}
	email := envOr("ORCINUS_E2E_ACME_EMAIL", "admin@"+domain)
	prod := os.Getenv("ORCINUS_E2E_ACME_PROD") != ""

	orcinus, _ := liveCluster(t, "orcinus-tls", 16476, "--http-port", "80", "--https-port", "443")

	certArgs := []string{"plugin", "install", "cert-manager", "--email", email}
	if !prod {
		certArgs = append(certArgs, "--staging")
	}
	if out, err := orcinus(certArgs...); err != nil {
		t.Fatalf("install cert-manager: %v\n%s", err, out)
	}

	f := writeCompose(t, `
services:
  web:
    image: traefik/whoami:v1.10
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: `+domain+`
    x-orcinus-tls: letsencrypt
`)
	if out, err := orcinus("deploy", "-f", f, "--project", "tls"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	certIssuer := func() string {
		out, _ := runcOut("bash", "-lc",
			"echo | openssl s_client -connect "+domain+":443 -servername "+domain+
				" 2>/dev/null | openssl x509 -noout -issuer")
		return out
	}

	// Wait until the *served* cert is from Let's Encrypt — not Traefik's default
	// self-signed cert (which `curl -k` would otherwise accept immediately).
	waitFor(t, 5*time.Minute, "Let's Encrypt cert served over HTTPS", func() bool {
		body, err := runcOut("curl", "-sk", "-m", "10", "https://"+domain+"/")
		if err != nil || !strings.Contains(body, "Hostname") {
			return false
		}
		return strings.Contains(certIssuer(), "Let's Encrypt")
	})
	t.Logf("issued cert: %s", strings.TrimSpace(certIssuer()))

	if prod {
		// Trusted chain: curl without -k must succeed.
		if out, err := runcOut("curl", "-sf", "-m", "10", "https://"+domain+"/"); err != nil {
			t.Fatalf("trusted HTTPS failed: %v\n%s", err, out)
		}
	}
}

// TestLiveSecret: create/ls/rm a generic secret via the CLI.
func TestLiveSecret(t *testing.T) {
	requireLive(t)
	orcinus, kubectl := liveCluster(t, "orcinus-sec", 16481)
	if out, err := orcinus("secret", "create", "app-config", "--from-literal", "FOO=bar", "--from-literal", "BAZ=qux"); err != nil {
		t.Fatalf("secret create: %v\n%s", err, out)
	}
	if got, _ := kubectl("get", "secret", "app-config", "-o", "jsonpath={.data.FOO}"); got == "" {
		t.Errorf("secret app-config missing key FOO")
	}
	if out, _ := orcinus("secret", "ls"); !strings.Contains(out, "app-config") {
		t.Errorf("secret ls missing app-config:\n%s", out)
	}
	if out, err := orcinus("secret", "rm", "app-config"); err != nil {
		t.Fatalf("secret rm: %v\n%s", err, out)
	}
	if _, err := kubectl("get", "secret", "app-config"); err == nil {
		t.Errorf("secret app-config should be gone")
	}
}

// TestLiveCustomCert: a bring-your-own TLS cert (secret create-tls) served via
// Ingress with x-orcinus-tls-secret — no external domain needed (uses --resolve).
func TestLiveCustomCert(t *testing.T) {
	requireLive(t)
	orcinus, _ := liveCluster(t, "orcinus-cc", 16482, "--http-port", "8081", "--https-port", "8444")

	// Generate a self-signed cert for cc.local.
	dir := t.TempDir()
	cert, key := dir+"/tls.crt", dir+"/tls.key"
	if out, err := runcOut("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
		"-keyout", key, "-out", cert, "-days", "2",
		"-subj", "/CN=cc.local", "-addext", "subjectAltName=DNS:cc.local"); err != nil {
		t.Fatalf("openssl: %v\n%s", err, out)
	}
	if out, err := orcinus("secret", "create-tls", "cc-cert", "--cert", cert, "--key", key); err != nil {
		t.Fatalf("secret create-tls: %v\n%s", err, out)
	}

	f := writeCompose(t, `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: cc.local
    x-orcinus-tls-secret: cc-cert
`)
	if out, err := orcinus("deploy", "-f", f, "--project", "cc"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	// HTTPS on 8444 must serve our self-signed cert (issuer CN=cc.local),
	// not Traefik's default.
	waitFor(t, 90*time.Second, "custom cert served", func() bool {
		out, err := runcOut("bash", "-lc",
			"echo | openssl s_client -connect 127.0.0.1:8444 -servername cc.local 2>/dev/null | openssl x509 -noout -issuer")
		return err == nil && strings.Contains(out, "cc.local")
	})
	// And the app is reachable over that TLS.
	if out, err := runcOut("curl", "-sk", "--resolve", "cc.local:8444:127.0.0.1", "-m", "10", "https://cc.local:8444/"); err != nil || !strings.Contains(out, "nginx") {
		t.Fatalf("https via custom cert failed: %v\n%s", err, out)
	}
}

// TestLiveEmbeddedRuntime proves the M3 embed+exec path: build the single
// self-contained orcinus binary (with the embedded runtime), run it as a bare
// host (a clean ubuntu container) via `orcinus runtime server`, and verify a
// real cluster + workload come up — no runtime image involved. Skipped unless
// ORCINUS_E2E_LIVE is set and the embed asset is present (`make orcinus-embedded`).
func TestLiveEmbeddedRuntime(t *testing.T) {
	requireLive(t)
	root := repoRoot()
	if _, err := os.Stat(root + "/pkg/runtime/assets/k3s"); err != nil {
		t.Skip("embedded runtime asset missing — run `make orcinus-embedded` first")
	}
	bin := t.TempDir() + "/orcinus-embedded"
	build := exec.Command("go", "build", "-tags", "embedruntime", "-o", bin, "./cmd/orcinus")
	build.Dir = root
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build embedded orcinus: %v\n%s", err, out)
	}

	docker := strings.Fields(envOr("ORCINUS_E2E_DOCKER", "docker"))
	const name = "orcinus-embed-e2e"
	_, _ = runcOut(append(docker, "rm", "-f", name)...)
	t.Cleanup(func() { _, _ = runcOut(append(docker, "rm", "-f", name)...) })

	// Run the single binary as PID 1 in a plain ubuntu container (the "bare
	// host"), using its `runtime server` passthrough to the embedded runtime.
	if out, err := runcOut(append(docker, "run", "-d", "--privileged", "--name", name,
		"-v", bin+":/usr/local/bin/orcinus:ro",
		"-v", "/etc/ssl/certs:/etc/ssl/certs:ro",
		"ubuntu:22.04", "orcinus", "runtime", "server",
		"--snapshotter=native", // nested-docker workaround; real hosts use overlayfs
		"--disable", "traefik", "--disable", "servicelb", "--disable", "metrics-server",
		"--write-kubeconfig-mode", "644")...); err != nil {
		t.Fatalf("start embedded runtime: %v\n%s", err, out)
	}
	kubectl := func(args ...string) (string, error) {
		return runcOut(append(append(docker, "exec", name, "orcinus", "runtime", "kubectl"), args...)...)
	}

	waitFor(t, 120*time.Second, "embedded-runtime node Ready", func() bool {
		out, _ := kubectl("get", "nodes", "--no-headers")
		return strings.Contains(out, " Ready")
	})
	if out, err := kubectl("create", "deployment", "web", "--image=nginx:1.27"); err != nil {
		t.Fatalf("create deployment: %v\n%s", err, out)
	}
	waitFor(t, 180*time.Second, "pod Running on embedded runtime", func() bool {
		out, _ := kubectl("get", "pods", "-l", "app=web", "--no-headers")
		f := strings.Fields(out)
		return len(f) >= 3 && f[1] == "1/1" && f[2] == "Running"
	})
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

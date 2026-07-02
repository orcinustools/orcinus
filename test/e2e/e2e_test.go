// Package e2e contains end-to-end tests that exercise the built orcinus binary.
//
//   - TestConvert* run offline (no cluster) and assert the generated manifests.
//   - TestLive* (live_test.go) boot a real single-node k3s in Docker.
package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// orcinusBin is the path to the binary built by TestMain.
var orcinusBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "orcinus-e2e-bin-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	orcinusBin = filepath.Join(tmp, "orcinus")
	build := exec.Command("go", "build", "-o", orcinusBin, "./cmd/orcinus")
	build.Dir = repoRoot()
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		panic("build failed: " + err.Error() + "\n" + string(out))
	}
	os.Exit(m.Run())
}

// repoRoot returns the module root (two levels up from test/e2e).
func repoRoot() string {
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func composeFixture() string {
	return filepath.Join(repoRoot(), "examples", "docker-compose.yml")
}

// TestConvertToDir runs `orcinus deploy --dry-run -o <dir>` against the example
// compose file and asserts the generated single-node manifests.
func TestConvertToDir(t *testing.T) {
	outDir := t.TempDir()
	cmd := exec.Command(orcinusBin, "deploy",
		"-f", composeFixture(),
		"--dry-run", "-o", outDir,
		"--project", "e2e",
	)
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deploy failed: %v\n%s", err, out)
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	for _, want := range []string{
		"deployment-web.yaml",
		"statefulset-db.yaml",
		"service-web.yaml",
		"ingress-web.yaml",
		"persistentvolumeclaim-dbdata.yaml",
		"secret-db-secret.yaml",
	} {
		if !got[want] {
			t.Errorf("missing generated manifest %s (got: %v)", want, keys(got))
		}
	}

	// Deep-check the Deployment.
	b, err := os.ReadFile(filepath.Join(outDir, "deployment-web.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var dep appsv1.Deployment
	if err := yaml.Unmarshal(b, &dep); err != nil {
		t.Fatalf("parse deployment: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 2 {
		t.Errorf("web replicas = %v, want 2", dep.Spec.Replicas)
	}
	if dep.Labels["app.kubernetes.io/managed-by"] != "orcinus" {
		t.Errorf("web missing orcinus managed-by label")
	}

	// The Secret must carry the extracted password.
	sb, err := os.ReadFile(filepath.Join(outDir, "secret-db-secret.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := yaml.Unmarshal(sb, &sec); err != nil {
		t.Fatalf("parse secret: %v", err)
	}
	if _, ok := sec.Data["POSTGRES_PASSWORD"]; !ok {
		t.Errorf("secret missing POSTGRES_PASSWORD")
	}
}

// TestConvertStdinAndForce verifies stdin input and `--as compose`.
func TestConvertStdinAndForce(t *testing.T) {
	cmd := exec.Command(orcinusBin, "deploy", "-f", "-", "--as", "compose", "--dry-run", "--project", "s")
	cmd.Dir = repoRoot()
	cmd.Stdin, _ = os.Open(composeFixture())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy from stdin failed: %v\n%s", err, out)
	}
	if !contains(string(out), "kind: Deployment") {
		t.Errorf("expected a Deployment in stdout, got:\n%s", out)
	}
}

// TestDeployDetectsOrcinusYml verifies that with no -f, `orcinus deploy` picks up
// orcinus.yml from the current directory.
func TestDeployDetectsOrcinusYml(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orcinus.yml"), []byte(
		"services:\n  web:\n    image: nginx:1.27\n    ports: [\"80:80\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(orcinusBin, "deploy", "--dry-run", "--project", "p")
	cmd.Dir = dir // run where orcinus.yml lives; no -f given
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy without -f failed: %v\n%s", err, out)
	}
	if !contains(string(out), "using orcinus.yml") {
		t.Errorf("expected orcinus.yml auto-detection, got:\n%s", out)
	}
	if !contains(string(out), "kind: Deployment") {
		t.Errorf("expected a Deployment from orcinus.yml, got:\n%s", out)
	}
}

// TestDeployFromURL verifies `orcinus deploy -f <url>` fetches over HTTP,
// like `kubectl apply -f <url>`.
func TestDeployFromURL(t *testing.T) {
	const doc = "services:\n  web:\n    image: nginx:1.27\n    ports: [\"80:80\"]\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(doc))
	}))
	defer srv.Close()

	cmd := exec.Command(orcinusBin, "deploy", "-f", srv.URL, "--dry-run", "--project", "url")
	cmd.Dir = repoRoot()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("deploy from URL failed: %v\n%s", err, out)
	}
	if !contains(string(out), "kind: Deployment") {
		t.Errorf("expected a Deployment from the fetched URL, got:\n%s", out)
	}
}

// TestExamplesRender dry-run-renders every examples/**/orcinus.yml so a broken
// example is caught in CI (no cluster needed).
func TestExamplesRender(t *testing.T) {
	root := repoRoot()
	matches, _ := filepath.Glob(filepath.Join(root, "examples", "*", "orcinus.yml"))
	matches = append(matches, filepath.Join(root, "examples", "orcinus.yml"))
	if len(matches) < 5 {
		t.Fatalf("expected several examples, found %d", len(matches))
	}
	for _, f := range matches {
		name := filepath.Base(filepath.Dir(f))
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(orcinusBin, "deploy", "-f", f, "--dry-run", "--project", "ex")
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("render %s failed: %v\n%s", f, err, out)
			}
			if !contains(string(out), "kind:") {
				t.Errorf("%s produced no manifests", f)
			}
		})
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

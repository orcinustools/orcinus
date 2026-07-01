// Package e2e contains end-to-end tests that exercise the built orcinus binary.
//
//   - TestConvert* run offline (no cluster) and assert the generated manifests.
//   - TestLive* (live_test.go) boot a real single-node k3s in Docker.
package e2e

import (
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

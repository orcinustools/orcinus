package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/layout"
)

func TestParseDockerfileClassification(t *testing.T) {
	cases := []struct {
		name     string
		df       string
		wantExec bool
	}{
		{"plain", "FROM scratch\nCOPY . /app\nENV X=1\nCMD [\"/app/run\"]\n", false},
		{"run", "FROM alpine\nRUN echo hi\n", true},
		{"multistage", "FROM golang AS b\nRUN go build\nFROM scratch\nCOPY --from=b /a /a\n", true},
		{"copy-from", "FROM scratch\nCOPY --from=alpine /etc/os-release /\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseDockerfile([]byte(tc.df), nil)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := p.needsExecutor(); got != tc.wantExec {
				t.Fatalf("needsExecutor()=%v want %v (reason=%q)", got, tc.wantExec, p.executorReason())
			}
		})
	}
}

// TestBuildNativeScratch builds an image FROM scratch (no network) and verifies
// the produced OCI layout carries the file layer and the runtime config.
func TestBuildNativeScratch(t *testing.T) {
	ctxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctxDir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	df := "FROM scratch\n" +
		"COPY hello.txt /data/hello.txt\n" +
		"ENV GREETING=hello\n" +
		"WORKDIR /data\n" +
		"EXPOSE 9000\n" +
		"ENTRYPOINT [\"/data/hello.txt\"]\n"
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	err := Build(context.Background(), Options{
		ContextDir: ctxDir,
		Tags:       []string{"scratch-demo:latest"},
		Platform:   "linux/amd64",
		Output:     Output{OCIDir: outDir},
		Stdout:     os.Stderr,
		Stderr:     os.Stderr,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	p, err := layout.FromPath(outDir)
	if err != nil {
		t.Fatalf("read layout: %v", err)
	}
	idx, err := p.ImageIndex()
	if err != nil {
		t.Fatal(err)
	}
	m, err := idx.IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Manifests) != 1 {
		t.Fatalf("want 1 manifest, got %d", len(m.Manifests))
	}
	img, err := idx.Image(m.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 {
		t.Fatalf("want 1 layer (the COPY), got %d", len(layers))
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	// The config's rootfs must describe exactly the image's layers, or the
	// image is not runnable (regression guard for the diff_ids/layers mismatch).
	if len(cf.RootFS.DiffIDs) != len(layers) {
		t.Fatalf("rootfs has %d diff_ids but image has %d layers", len(cf.RootFS.DiffIDs), len(layers))
	}
	if cf.OS != "linux" || cf.Architecture != "amd64" {
		t.Errorf("platform = %s/%s, want linux/amd64", cf.OS, cf.Architecture)
	}
	if cf.Config.WorkingDir != "/data" {
		t.Errorf("WorkingDir = %q, want /data", cf.Config.WorkingDir)
	}
	if len(cf.Config.Entrypoint) != 1 || cf.Config.Entrypoint[0] != "/data/hello.txt" {
		t.Errorf("Entrypoint = %v", cf.Config.Entrypoint)
	}
	if _, ok := cf.Config.ExposedPorts["9000/tcp"]; !ok {
		t.Errorf("ExposedPorts = %v, want 9000/tcp", cf.Config.ExposedPorts)
	}
	var hasEnv bool
	for _, e := range cf.Config.Env {
		if e == "GREETING=hello" {
			hasEnv = true
		}
	}
	if !hasEnv {
		t.Errorf("Env = %v, want GREETING=hello", cf.Config.Env)
	}
}

func TestParseKVHelpers(t *testing.T) {
	os, arch, variant := splitPlatform("")
	if os != "linux" || arch != "amd64" || variant != "" {
		t.Errorf("default platform = %s/%s/%s", os, arch, variant)
	}
	os, arch, variant = splitPlatform("linux/arm64/v8")
	if os != "linux" || arch != "arm64" || variant != "v8" {
		t.Errorf("parsed platform = %s/%s/%s", os, arch, variant)
	}
}

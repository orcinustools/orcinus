package cli

import (
	"io"
	"strings"
	"testing"
)

func TestConfigCmdSurface(t *testing.T) {
	cmd, _, err := NewRootCmd().Find([]string{"config"})
	if err != nil {
		t.Fatalf("find config: %v", err)
	}
	if cmd.Name() != "config" {
		t.Fatalf("resolved to %q", cmd.Name())
	}
	for _, tc := range []struct{ flag, want string }{
		{"format", "summary"},
		{"namespace", "default"},
		{"show-secrets", "false"},
	} {
		f := cmd.Flags().Lookup(tc.flag)
		if f == nil {
			t.Fatalf("--%s not registered", tc.flag)
		}
		if f.DefValue != tc.want {
			t.Errorf("--%s default = %s, want %s", tc.flag, f.DefValue, tc.want)
		}
	}
	// The project argument is optional: it falls back to the directory name.
	if err := cmd.Args(cmd, nil); err != nil {
		t.Errorf("config with no argument should be allowed: %v", err)
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("config takes at most one project")
	}
}

// An unknown --format must fail before any cluster call, so a typo reports
// itself instead of surfacing as a kubeconfig error.
func TestConfigRejectsUnknownFormat(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"config", "demo", "--format", "yamlish", "--kubeconfig", "/nonexistent/kubeconfig"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown format")
	}
	if !strings.Contains(err.Error(), "unknown format") {
		t.Errorf("format should be validated before the cluster is contacted, got: %v", err)
	}
}

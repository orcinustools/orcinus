package cli

import "testing"

// Deploy must not destroy data unless asked: --prune-pvc is opt-in, while the
// general --prune stays on.
func TestDeployPruneDefaults(t *testing.T) {
	cmd, _, err := NewRootCmd().Find([]string{"deploy"})
	if err != nil {
		t.Fatalf("find deploy: %v", err)
	}
	for _, tc := range []struct{ flag, want string }{
		{"prune", "true"},
		{"prune-pvc", "false"},
	} {
		f := cmd.Flags().Lookup(tc.flag)
		if f == nil {
			t.Fatalf("--%s not registered", tc.flag)
		}
		if f.DefValue != tc.want {
			t.Errorf("--%s default = %s, want %s", tc.flag, f.DefValue, tc.want)
		}
	}
}

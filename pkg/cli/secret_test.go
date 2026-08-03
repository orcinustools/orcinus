package cli

import (
	"strings"
	"testing"
)

// TestSecretSubcommands: get and set are reachable, and the flags that gate
// printing values / merging keys are registered with safe defaults.
func TestSecretSubcommands(t *testing.T) {
	for _, tc := range []struct {
		path  []string
		flag  string
		defal string
	}{
		{[]string{"secret", "get"}, "show-values", "false"},
		{[]string{"secret", "set"}, "namespace", "default"},
		{[]string{"secret", "create"}, "namespace", "default"},
	} {
		cmd, _, err := NewRootCmd().Find(tc.path)
		if err != nil {
			t.Fatalf("find %v: %v", tc.path, err)
		}
		if cmd.Name() != tc.path[len(tc.path)-1] {
			t.Fatalf("%v resolved to %q", tc.path, cmd.Name())
		}
		f := cmd.Flags().Lookup(tc.flag)
		if f == nil {
			t.Fatalf("%v: --%s not registered", tc.path, tc.flag)
		}
		if f.DefValue != tc.defal {
			t.Errorf("%v: --%s default = %s, want %s", tc.path, tc.flag, f.DefValue, tc.defal)
		}
	}
}

// TestSecretCreateAndSetDocumentTheDifference: the destructive one has to say
// so, since `create` on an existing name drops the keys it was not given.
func TestSecretCreateAndSetDocumentTheDifference(t *testing.T) {
	create, _, err := NewRootCmd().Find([]string{"secret", "create"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(create.Long, "REPLACED") {
		t.Errorf("`secret create` help should warn that it replaces:\n%s", create.Long)
	}
	set, _, err := NewRootCmd().Find([]string{"secret", "set"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(set.Long, "restart") {
		t.Errorf("`secret set` help should mention restarting to pick up new values:\n%s", set.Long)
	}
}

// TestSecretRequiresLiteral: both writers reject an empty write rather than
// clearing the Secret.
func TestSecretRequiresLiteral(t *testing.T) {
	if _, err := parseLiterals(nil); err == nil {
		t.Error("parseLiterals(nil) should fail rather than write an empty secret")
	}
	if _, err := parseLiterals([]string{"NOEQUALS"}); err == nil {
		t.Error("a literal without = should be rejected")
	}
	if _, err := parseLiterals([]string{"=novalue"}); err == nil {
		t.Error("a literal with an empty key should be rejected")
	}
	data, err := parseLiterals([]string{"K=v", "EMPTY=", "WITH=a=b"})
	if err != nil {
		t.Fatalf("parseLiterals: %v", err)
	}
	if string(data["K"]) != "v" {
		t.Errorf("K = %q, want v", data["K"])
	}
	if _, ok := data["EMPTY"]; !ok {
		t.Error("an empty value is still a value")
	}
	if string(data["WITH"]) != "a=b" {
		t.Errorf("WITH = %q, want a=b (only the first = splits)", data["WITH"])
	}
}

// TestSummarizeKeys keeps the ls row scannable when a Secret holds many keys.
func TestSummarizeKeys(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, "-"},
		{[]string{"A"}, "A"},
		{[]string{"A", "B", "C"}, "A, B, C"},
		{[]string{"A", "B", "C", "D"}, "A, B, C, +1 more"},
		{[]string{"A", "B", "C", "D", "E"}, "A, B, C, +2 more"},
	} {
		if got := summarizeKeys(tc.in); got != tc.want {
			t.Errorf("summarizeKeys(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

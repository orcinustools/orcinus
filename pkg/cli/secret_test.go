package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
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

// TestSecretRequiresInput: both writers reject an empty write rather than
// clearing the Secret.
func TestSecretRequiresInput(t *testing.T) {
	if _, err := secretData(nil, nil); err == nil {
		t.Error("no input at all should fail rather than write an empty secret")
	}
	if _, err := secretData([]string{"NOEQUALS"}, nil); err == nil {
		t.Error("a literal without = should be rejected")
	}
	if _, err := secretData([]string{"=novalue"}, nil); err == nil {
		t.Error("a literal with an empty key should be rejected")
	}
	if _, err := secretData([]string{"bad key=v"}, nil); err == nil {
		t.Error("a literal whose key Kubernetes would reject should be caught here")
	}
	data, err := secretData([]string{"K=v", "EMPTY=", "WITH=a=b"}, nil)
	if err != nil {
		t.Fatalf("secretData: %v", err)
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

// writeFile creates a file with exact bytes and returns its path.
func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSecretDataFromFile: the key defaults to the file name, and the bytes are
// taken verbatim — including the trailing newline that `$(cat f)` eats.
func TestSecretDataFromFile(t *testing.T) {
	dir := t.TempDir()
	pem := []byte("-----BEGIN KEY-----\nabc\n-----END KEY-----\n")
	p := writeFile(t, dir, "api.key", pem)

	data, err := secretData(nil, []string{p})
	if err != nil {
		t.Fatalf("secretData: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("data = %v, want one key", keysOf(data))
	}
	got, ok := data["api.key"]
	if !ok {
		t.Fatalf("keys = %v, want the file's base name", keysOf(data))
	}
	if string(got) != string(pem) {
		t.Errorf("content = %q, want the file byte-for-byte %q", got, pem)
	}
}

// TestSecretDataFromFileBinary: raw bytes, not text — a value routed through a
// shell argument could not carry these at all.
func TestSecretDataFromFileBinary(t *testing.T) {
	dir := t.TempDir()
	blob := []byte{'A', 0x00, 'B', 0xFF, 'C'}
	p := writeFile(t, dir, "blob.bin", blob)

	data, err := secretData(nil, []string{"blob=" + p})
	if err != nil {
		t.Fatalf("secretData: %v", err)
	}
	got, ok := data["blob"]
	if !ok {
		t.Fatalf("keys = %v, want the explicit key", keysOf(data))
	}
	if !bytes.Equal(got, blob) {
		t.Errorf("content = %v, want %v", got, blob)
	}
}

// TestSecretDataFromDir: every regular file becomes a key; nested directories
// are skipped rather than flattened.
func TestSecretDataFromDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "one.conf", []byte("1"))
	writeFile(t, dir, "two.conf", []byte("2"))
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "nested"), "three.conf", []byte("3"))

	data, err := secretData(nil, []string{dir})
	if err != nil {
		t.Fatalf("secretData: %v", err)
	}
	if len(data) != 2 || string(data["one.conf"]) != "1" || string(data["two.conf"]) != "2" {
		t.Fatalf("keys = %v, want just one.conf and two.conf", keysOf(data))
	}
	if _, leaked := data["three.conf"]; leaked {
		t.Error("a nested directory's file leaked into the Secret")
	}
}

// TestSecretDataCombined: literals and files merge, and several --from-file
// entries all land (a loop that returned after the first would drop the rest).
func TestSecretDataCombined(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.txt", []byte("A"))
	b := writeFile(t, dir, "b.txt", []byte("B"))

	data, err := secretData([]string{"LIT=v"}, []string{a, b})
	if err != nil {
		t.Fatalf("secretData: %v", err)
	}
	if len(data) != 3 {
		t.Fatalf("keys = %v, want LIT, a.txt and b.txt", keysOf(data))
	}
	if string(data["LIT"]) != "v" || string(data["a.txt"]) != "A" || string(data["b.txt"]) != "B" {
		t.Errorf("wrong values: %v", keysOf(data))
	}
}

// TestSecretDataFromFileErrors: every way of getting it wrong reports which
// flag to fix, instead of writing a half-built Secret.
func TestSecretDataFromFileErrors(t *testing.T) {
	dir := t.TempDir()
	ok := writeFile(t, dir, "ok.txt", []byte("x"))
	odd := writeFile(t, dir, "not a key.txt", []byte("x"))
	empty := filepath.Join(dir, "emptydir")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		literals  []string
		files     []string
		wantInErr string
	}{
		{"nothing at all", nil, nil, "--from-file"},
		{"missing file", nil, []string{filepath.Join(dir, "nope.txt")}, "nope.txt"},
		{"file name is not a usable key", nil, []string{odd}, "not a usable key"},
		{"key given for a directory", nil, []string{"k=" + dir}, "cannot be given for a directory"},
		{"empty directory", nil, []string{empty}, "no files"},
		{"directory holding an unusable file name", nil, []string{dirWithOddName(t)}, "not a usable key"},
		{"file collides with a literal", []string{"ok.txt=v"}, []string{ok}, "already set"},
		{"two files collide", nil, []string{"k=" + ok, "k=" + ok}, "already set"},
	} {
		_, err := secretData(tc.literals, tc.files)
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantInErr) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.name, err, tc.wantInErr)
		}
	}
}

// TestSecretCreateAndSetTakeFromFile: both writers expose the flag.
func TestSecretCreateAndSetTakeFromFile(t *testing.T) {
	for _, path := range [][]string{{"secret", "create"}, {"secret", "set"}} {
		cmd, _, err := NewRootCmd().Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if cmd.Flags().Lookup("from-file") == nil {
			t.Errorf("%v: --from-file not registered", path)
		}
	}
}

func keysOf(data map[string][]byte) []string {
	out := make([]string, 0, len(data))
	for k := range data {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dirWithOddName returns a directory holding a file whose name cannot be a
// Secret key — the directory scan has to catch it too, not just a bare path.
func dirWithOddName(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "fine.conf", []byte("x"))
	writeFile(t, dir, "not a key.conf", []byte("x"))
	return dir
}

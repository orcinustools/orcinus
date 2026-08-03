package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/orcinustools/orcinus/pkg/compose"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestBuildDockerConfigJSON(t *testing.T) {
	raw, err := BuildDockerConfigJSON("registry.example.com", "alice", "s3cret", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Auths map[string]struct {
			Username, Password, Auth, Email string
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid dockerconfigjson: %v", err)
	}
	e, ok := doc.Auths["registry.example.com"]
	if !ok {
		t.Fatalf("no entry for registry host: %s", raw)
	}
	if e.Username != "alice" || e.Password != "s3cret" || e.Email != "alice@example.com" {
		t.Errorf("entry mismatch: %+v", e)
	}
	want := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if e.Auth != want {
		t.Errorf("auth = %q, want %q", e.Auth, want)
	}
}

func TestBuildDockerConfigJSONNoEmail(t *testing.T) {
	raw, err := BuildDockerConfigJSON("ghcr.io", "bob", "tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got == "" || !json.Valid(raw) {
		t.Fatalf("invalid json: %q", got)
	}
	// email must be omitted when empty
	var doc map[string]map[string]map[string]string
	_ = json.Unmarshal(raw, &doc)
	if _, has := doc["auths"]["ghcr.io"]["email"]; has {
		t.Errorf("email should be omitted when empty")
	}
}

func secretApplier(objs ...runtime.Object) *Applier {
	return &Applier{clientset: k8sfake.NewSimpleClientset(objs...)}
}

func existingSecret(name string, typ corev1.SecretType, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels:    map[string]string{compose.LabelManagedBy: compose.ManagedByValue},
		},
		Type: typ,
		Data: data,
	}
}

// readSecret reads a Secret's data back through the API, as a caller would.
func readSecret(t *testing.T, a *Applier, name string) map[string]string {
	t.Helper()
	got, err := a.GetSecret(context.Background(), testNS, name)
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	out := map[string]string{}
	for k, v := range got.Data {
		out[k] = string(v)
	}
	return out
}

// TestMergeSecretKeepsOtherKeys is the whole point of MergeSecret: changing one
// key must not take the rest of the Secret with it.
func TestMergeSecretKeepsOtherKeys(t *testing.T) {
	a := secretApplier(existingSecret("app-secret", corev1.SecretTypeOpaque, map[string][]byte{
		"DB_PASS": []byte("old"),
		"API_KEY": []byte("keep-me"),
	}))
	err := a.MergeSecret(context.Background(), testNS, "app-secret", corev1.SecretTypeOpaque,
		map[string][]byte{"DB_PASS": []byte("new")})
	if err != nil {
		t.Fatalf("MergeSecret: %v", err)
	}
	got := readSecret(t, a, "app-secret")
	if got["DB_PASS"] != "new" {
		t.Errorf("DB_PASS = %q, want the new value", got["DB_PASS"])
	}
	if got["API_KEY"] != "keep-me" {
		t.Errorf("API_KEY = %q, want it untouched", got["API_KEY"])
	}
	if len(got) != 2 {
		t.Errorf("secret has %d keys, want 2: %v", len(got), got)
	}
}

// TestApplySecretReplaces pins the contrast MergeSecret exists for: ApplySecret
// writes the data wholesale, so keys left out are dropped.
func TestApplySecretReplaces(t *testing.T) {
	a := secretApplier(existingSecret("app-secret", corev1.SecretTypeOpaque, map[string][]byte{
		"DB_PASS": []byte("old"),
		"API_KEY": []byte("dropped"),
	}))
	err := a.ApplySecret(context.Background(), testNS, "app-secret", corev1.SecretTypeOpaque,
		map[string][]byte{"DB_PASS": []byte("new")})
	if err != nil {
		t.Fatalf("ApplySecret: %v", err)
	}
	got := readSecret(t, a, "app-secret")
	if len(got) != 1 || got["DB_PASS"] != "new" {
		t.Fatalf("secret = %v, want only DB_PASS=new", got)
	}
}

// TestMergeSecretCreatesWhenAbsent: set on a name that does not exist yet is a
// create, not a failure.
func TestMergeSecretCreatesWhenAbsent(t *testing.T) {
	a := secretApplier()
	err := a.MergeSecret(context.Background(), testNS, "fresh", corev1.SecretTypeOpaque,
		map[string][]byte{"K": []byte("v")})
	if err != nil {
		t.Fatalf("MergeSecret on a missing secret: %v", err)
	}
	if got := readSecret(t, a, "fresh"); got["K"] != "v" {
		t.Fatalf("secret = %v, want K=v", got)
	}
}

// TestMergeSecretKeepsType: merging into a TLS or registry Secret must not
// retype it to Opaque, which would break every consumer of it.
func TestMergeSecretKeepsType(t *testing.T) {
	a := secretApplier(existingSecret("mysite-cert", corev1.SecretTypeTLS, map[string][]byte{
		"tls.crt": []byte("cert"),
		"tls.key": []byte("key"),
	}))
	err := a.MergeSecret(context.Background(), testNS, "mysite-cert", corev1.SecretTypeOpaque,
		map[string][]byte{"tls.crt": []byte("renewed")})
	if err != nil {
		t.Fatalf("MergeSecret: %v", err)
	}
	got, err := a.GetSecret(context.Background(), testNS, "mysite-cert")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if got.Type != string(corev1.SecretTypeTLS) {
		t.Fatalf("type = %q, want it left as %q", got.Type, corev1.SecretTypeTLS)
	}
	if string(got.Data["tls.key"]) != "key" {
		t.Errorf("tls.key = %q, want it untouched", got.Data["tls.key"])
	}
}

// TestGetSecretKeyNames: keys come back sorted, so output does not shuffle.
func TestGetSecretKeyNames(t *testing.T) {
	a := secretApplier(existingSecret("app-secret", corev1.SecretTypeOpaque, map[string][]byte{
		"ZED": []byte("1"), "API_KEY": []byte("2"), "DB_PASS": []byte("3"),
	}))
	got, err := a.GetSecret(context.Background(), testNS, "app-secret")
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	want := []string{"API_KEY", "DB_PASS", "ZED"}
	keys := got.KeyNames()
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v, want %v", keys, want)
		}
	}
	if !got.ManagedBy {
		t.Error("ManagedBy = false, want true for an orcinus-labelled Secret")
	}
}

// TestListSecretsReportsKeyNames: ls needs the names, not just how many.
func TestListSecretsReportsKeyNames(t *testing.T) {
	a := secretApplier(existingSecret("app-secret", corev1.SecretTypeOpaque, map[string][]byte{
		"B": []byte("1"), "A": []byte("2"),
	}))
	list, err := a.ListSecrets(context.Background(), testNS)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d secrets, want 1", len(list))
	}
	s := list[0]
	if s.Keys != 2 {
		t.Errorf("Keys = %d, want 2", s.Keys)
	}
	if len(s.KeyNames) != 2 || s.KeyNames[0] != "A" || s.KeyNames[1] != "B" {
		t.Errorf("KeyNames = %v, want [A B]", s.KeyNames)
	}
}

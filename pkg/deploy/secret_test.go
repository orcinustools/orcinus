package deploy

import (
	"encoding/base64"
	"encoding/json"
	"testing"
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

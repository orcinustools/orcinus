package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// drive runs a set of JSON-RPC request lines through the server and returns the
// decoded responses keyed by id.
func drive(t *testing.T, allowWrite bool, lines ...string) map[float64]map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := New("", allowWrite).Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	res := map[float64]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		res[m["id"].(float64)] = m
	}
	return res
}

func toolNames(resp map[string]any) map[string]bool {
	names := map[string]bool{}
	result := resp["result"].(map[string]any)
	for _, tt := range result["tools"].([]any) {
		names[tt.(map[string]any)["name"].(string)] = true
	}
	return names
}

func TestInitializeAndTools(t *testing.T) {
	r := drive(t, false,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	si := r[1]["result"].(map[string]any)["serverInfo"].(map[string]any)
	if si["name"] != "orcinus" {
		t.Errorf("serverInfo name = %v", si["name"])
	}
	names := toolNames(r[2])
	if !names["convert"] || !names["orcinus_skills"] {
		t.Errorf("read-only tools missing: %v", names)
	}
	if names["deploy"] {
		t.Error("deploy must NOT be exposed without --allow-write")
	}
}

func TestAllowWriteExposesDeploy(t *testing.T) {
	r := drive(t, true, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if !toolNames(r[1])["deploy"] {
		t.Error("deploy should be exposed with allowWrite")
	}
}

func TestConvertTool(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"convert","arguments":{"source":"services:\n  web:\n    image: nginx:1.27\n    ports: [\"80\"]\n"}}}`
	r := drive(t, false, body)
	result := r[1]["result"].(map[string]any)
	if result["isError"].(bool) {
		t.Fatalf("convert errored: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "kind: Deployment") {
		t.Errorf("convert output missing Deployment:\n%s", text)
	}
}

func TestWriteToolBlocked(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deploy","arguments":{"source":"x"}}}`
	r := drive(t, false, body)
	result := r[1]["result"].(map[string]any)
	if !result["isError"].(bool) {
		t.Error("deploy without allowWrite should be an error result")
	}
}

func TestResources(t *testing.T) {
	r := drive(t, false,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"orcinus://skills/overview"}}`,
	)
	if len(r[1]["result"].(map[string]any)["resources"].([]any)) < 5 {
		t.Error("too few skill resources")
	}
	txt := r[2]["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(txt, "orcinus") {
		t.Error("resource read empty")
	}
}

func TestUnknownMethod(t *testing.T) {
	r := drive(t, false, `{"jsonrpc":"2.0","id":1,"method":"bogus/method"}`)
	if r[1]["error"] == nil {
		t.Error("unknown method should return a JSON-RPC error")
	}
}

func TestNotificationNoResponse(t *testing.T) {
	var out bytes.Buffer
	_ = New("", false).Serve(strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"), &out)
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("notification should produce no response, got %q", out.String())
	}
}

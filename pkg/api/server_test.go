package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(token string) http.Handler {
	return New(Config{Token: token}).Handler()
}

func TestHealthAndVersionOpen(t *testing.T) {
	h := testServer("secret") // token set, but these routes must stay open
	for _, path := range []string{"/healthz", "/version", "/openapi.json", "/docs"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestAuthEnforcedOnAPI(t *testing.T) {
	h := testServer("secret")

	// Without a token → 401.
	req := httptest.NewRequest("GET", "/api/v1/plugins", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", rec.Code)
	}

	// With the right token → 200 (plugin list is static, no cluster needed).
	req = httptest.NewRequest("GET", "/api/v1/plugins", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cert-manager") {
		t.Errorf("plugins list should include built-ins:\n%s", rec.Body.String())
	}
}

func TestNoAuthWhenTokenEmpty(t *testing.T) {
	h := testServer("") // no token configured → API open
	req := httptest.NewRequest("GET", "/api/v1/plugins", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("open API = %d, want 200", rec.Code)
	}
}

func TestConvertOffline(t *testing.T) {
	h := testServer("")
	body := `{"source":"services:\n  web:\n    image: nginx:1.27\n    ports: [\"80\"]\n","project":"demo"}`
	req := httptest.NewRequest("POST", "/api/v1/convert", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("convert = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "kind: Deployment") {
		t.Errorf("convert output missing Deployment:\n%s", rec.Body.String())
	}
}

func TestConvertRawYAMLBody(t *testing.T) {
	h := testServer("")
	yaml := "services:\n  web:\n    image: nginx:1.27\n    ports: [\"80\"]\n"
	req := httptest.NewRequest("POST", "/api/v1/convert?project=demo", strings.NewReader(yaml))
	req.Header.Set("Content-Type", "application/yaml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("convert(raw) = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "kind: Service") {
		t.Errorf("convert(raw) output missing Service:\n%s", rec.Body.String())
	}
}

func TestConvertBadInput(t *testing.T) {
	h := testServer("")
	req := httptest.NewRequest("POST", "/api/v1/convert", strings.NewReader(`{"source":"not: [valid"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad convert = %d, want 400", rec.Code)
	}
}

func TestUnknownPlugin404(t *testing.T) {
	h := testServer("")
	req := httptest.NewRequest("POST", "/api/v1/plugins/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown plugin = %d, want 404", rec.Code)
	}
}

func TestOpenAPIJSONValid(t *testing.T) {
	h := testServer("")
	req := httptest.NewRequest("GET", "/openapi.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("openapi content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"openapi"`) || !strings.Contains(rec.Body.String(), "/api/v1/deploy") {
		t.Errorf("openapi.json missing expected content")
	}
}

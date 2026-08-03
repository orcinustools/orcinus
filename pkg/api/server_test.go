package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testServer builds the handler with a kubeconfig that cannot resolve, so every
// cluster-touching route fails with 503 instead of reaching whatever cluster the
// machine happens to point at. Without this, Config{Kubeconfig: ""} falls
// through to $KUBECONFIG, then ~/.orcinus/kubeconfig — so a test that POSTs a
// Secret writes it to the developer's real cluster.
func testServer(token string) http.Handler {
	return New(Config{Token: token, Kubeconfig: unreachableKubeconfig}).Handler()
}

const unreachableKubeconfig = "/nonexistent/orcinus-test/kubeconfig"

// TestTestServerCannotReachACluster guards the isolation the rest of this file
// depends on: if applier() ever starts succeeding here, these tests are writing
// to a real cluster.
func TestTestServerCannotReachACluster(t *testing.T) {
	h := testServer("")
	req := httptest.NewRequest("GET", "/api/v1/secrets", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/secrets = %d, want 503; the test server can reach a cluster", rec.Code)
	}
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

// TestSecretRoutesWired: the CLI's get/set/create-tls have HTTP equivalents.
// Offline these reach the handler and fail for want of a cluster (503) or on
// validation (400) — what matters is that they are not 404/405 any more.
func TestSecretRoutesWired(t *testing.T) {
	h := testServer("")
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"get", "GET", "/api/v1/secrets/app-secret", ""},
		{"get with values", "GET", "/api/v1/secrets/app-secret?showValues=true", ""},
		{"set", "PATCH", "/api/v1/secrets/app-secret", `{"data":{"K":"v"}}`},
		{"create-tls", "POST", "/api/v1/secrets/tls", `{"name":"c","cert":"x","key":"y"}`},
	} {
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s (%s) = %d, route not registered", tc.method, tc.path, tc.name, rec.Code)
		}
	}
}

// TestSecretWritesValidateBeforeCluster: a bad body is rejected without needing
// a cluster, so a typo reports itself instead of surfacing as a kubeconfig error.
func TestSecretWritesValidateBeforeCluster(t *testing.T) {
	h := testServer("")
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"set with no data", "PATCH", "/api/v1/secrets/app-secret", `{}`},
		{"tls with no cert", "POST", "/api/v1/secrets/tls", `{"name":"c","key":"y"}`},
		{"tls with no name", "POST", "/api/v1/secrets/tls", `{"cert":"x","key":"y"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.name, rec.Code)
		}
	}
}

// TestOpenAPICoversSecretSurface: the spec is the published contract, so a new
// endpoint that is not in it is not really shipped.
func TestOpenAPICoversSecretSurface(t *testing.T) {
	h := testServer("")
	req := httptest.NewRequest("GET", "/openapi.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	spec := rec.Body.String()
	for _, want := range []string{
		"/api/v1/secrets/{name}", "/api/v1/secrets/tls",
		"SecretDetail", "TLSSecretRequest", "KeyNames", "showValues",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("openapi.json missing %q", want)
		}
	}
}

// TestConvertQueryParamsMatchJSONBody: the raw-body path takes its options from
// query params, and every DeployRequest field has to be read there too —
// pvcSize was silently dropped, so a PVC came out at the default size with no
// error, and a PVC cannot be resized afterwards.
func TestConvertQueryParamsMatchJSONBody(t *testing.T) {
	h := testServer("")
	const src = `services:
  db:
    image: postgres:16
    volumes:
      - data:/var/lib/postgresql/data
volumes:
  data:
`
	render := func(t *testing.T, method, path, body, contentType string) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", method, path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Raw YAML body + query params.
	viaQuery := render(t, "POST", "/api/v1/convert?project=p&pvcSize=7Gi", src, "text/yaml")
	if !strings.Contains(viaQuery, "7Gi") {
		t.Errorf("pvcSize query param ignored; rendered PVC is not 7Gi:\n%s", viaQuery)
	}

	// The JSON body path, for comparison.
	viaJSON := render(t, "POST", "/api/v1/convert",
		`{"source":`+jsonString(src)+`,"project":"p","pvcSize":"7Gi"}`, "application/json")
	if !strings.Contains(viaJSON, "7Gi") {
		t.Errorf("pvcSize in the JSON body ignored:\n%s", viaJSON)
	}
}

// jsonString quotes a string for embedding in a JSON literal.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

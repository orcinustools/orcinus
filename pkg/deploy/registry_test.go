package deploy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryBaseURL(t *testing.T) {
	cases := map[string]string{
		"registry.example.com":         "https://registry.example.com",
		"registry.example.com/":        "https://registry.example.com",
		"https://registry.example.com": "https://registry.example.com",
		"http://reg.local:5000":        "http://reg.local:5000",
		"ghcr.io":                      "https://ghcr.io",
		"docker.io":                    "https://registry-1.docker.io",
		"index.docker.io":              "https://registry-1.docker.io",
	}
	for in, want := range cases {
		if got := registryBaseURL(in); got != want {
			t.Errorf("registryBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBearerChallenge(t *testing.T) {
	realm, params := parseBearerChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull"`)
	if realm != "https://auth.docker.io/token" {
		t.Errorf("realm = %q", realm)
	}
	if params["service"] != "registry.docker.io" {
		t.Errorf("service = %q", params["service"])
	}
	if params["scope"] != "repository:library/nginx:pull" {
		t.Errorf("scope = %q", params["scope"])
	}
}

// basicRegistry serves /v2/ with HTTP basic auth.
func basicRegistry(user, pass string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.Header().Set("Www-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// bearerRegistry serves /v2/ with a token challenge and a /token endpoint.
func bearerRegistry(user, pass string) *httptest.Server {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"token":"abc123"}`))
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer abc123" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	return srv
}

func TestVerifyRegistryLogin(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		srv        *httptest.Server
		user, pass string
		wantErr    bool
	}{
		{"basic-ok", basicRegistry("me", "pw"), "me", "pw", false},
		{"basic-bad", basicRegistry("me", "pw"), "me", "nope", true},
		{"bearer-ok", bearerRegistry("me", "pw"), "me", "pw", false},
		{"bearer-bad", bearerRegistry("me", "pw"), "me", "nope", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer tc.srv.Close()
			host := strings.TrimPrefix(tc.srv.URL, "http://")
			err := VerifyRegistryLogin(ctx, "http://"+host, tc.user, tc.pass, false)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected success, got %v", err)
			}
		})
	}
}

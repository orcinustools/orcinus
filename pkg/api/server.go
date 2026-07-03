// Package api serves the orcinus HTTP REST API (`orcinus api`). It is a thin
// layer over the same packages the CLI uses (engine/deploy/plugin/cluster), so
// the API and CLI behave identically. See docs/API.md and the embedded OpenAPI
// spec (GET /openapi.json, interactive UI at GET /docs).
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/version"
)

// Config configures the API server.
type Config struct {
	// Token, if non-empty, is required as `Authorization: Bearer <token>` on all
	// /api/v1/* routes. Health, version, and the OpenAPI/docs routes stay open.
	Token string
	// Kubeconfig is the path used to reach the cluster (empty = default resolution).
	Kubeconfig string
}

// Server holds the API configuration and builds the HTTP handler.
type Server struct {
	cfg Config
}

// New returns a Server.
func New(cfg Config) *Server { return &Server{cfg: cfg} }

// Handler wires the routes and returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Open (unauthenticated) routes.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /openapi.json", s.handleOpenAPIJSON)
	mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPIYAML)
	mux.HandleFunc("GET /docs", s.handleDocs)

	// Authenticated API routes.
	mux.HandleFunc("POST /api/v1/convert", s.handleConvert)
	mux.HandleFunc("POST /api/v1/deploy", s.handleDeploy)
	mux.HandleFunc("GET /api/v1/projects", s.handleListProjects)
	mux.HandleFunc("GET /api/v1/projects/{project}/pods", s.handleProjectPods)
	mux.HandleFunc("DELETE /api/v1/projects/{project}", s.handleRemoveProject)
	mux.HandleFunc("POST /api/v1/projects/{project}/services/{service}/scale", s.handleScale)
	mux.HandleFunc("POST /api/v1/projects/{project}/services/{service}/rollback", s.handleRollback)
	mux.HandleFunc("GET /api/v1/secrets", s.handleListSecrets)
	mux.HandleFunc("POST /api/v1/secrets", s.handleCreateSecret)
	mux.HandleFunc("DELETE /api/v1/secrets/{name}", s.handleDeleteSecret)
	mux.HandleFunc("GET /api/v1/plugins", s.handleListPlugins)
	mux.HandleFunc("POST /api/v1/plugins/{name}", s.handleInstallPlugin)
	mux.HandleFunc("DELETE /api/v1/plugins/{name}", s.handleRemovePlugin)
	mux.HandleFunc("GET /api/v1/cluster", s.handleCluster)

	return s.recover(s.auth(mux))
}

// --- middleware ---

// auth enforces the bearer token (if configured) on /api/v1/* routes only.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != s.cfg.Token {
				writeErr(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// recover turns a handler panic into a 500 instead of crashing the server.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// applier builds a fresh cluster applier (returns a clear error if no cluster).
func (s *Server) applier() (*deploy.Applier, error) {
	cfg, err := deploy.LoadRESTConfig(s.cfg.Kubeconfig)
	if err != nil {
		return nil, err
	}
	return deploy.NewApplier(cfg)
}

// --- open handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":   version.Version,
		"gitCommit": version.GitCommit,
		"kompose":   version.KomposeRef,
	})
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

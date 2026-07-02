package api

import (
	_ "embed"
	"net/http"

	"sigs.k8s.io/yaml"
)

//go:embed openapi.yaml
var openapiYAML []byte

func (s *Server) handleOpenAPIYAML(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiYAML)
}

func (s *Server) handleOpenAPIJSON(w http.ResponseWriter, _ *http.Request) {
	j, err := yaml.YAMLToJSON(openapiYAML)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "invalid embedded openapi spec")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(j)
}

// handleDocs serves a minimal Swagger UI that loads /openapi.json.
func (s *Server) handleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerUIHTML))
}

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Orcinus API — Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui" });
  </script>
</body>
</html>`

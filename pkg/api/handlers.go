package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/orcinustools/orcinus/pkg/cluster"
	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/engine"
	"github.com/orcinustools/orcinus/pkg/plugin"

	corev1 "k8s.io/api/core/v1"
)

// DeployRequest is the JSON body for POST /api/v1/deploy and /convert. When the
// request Content-Type is not JSON, the raw body is the source and options come
// from query parameters.
type DeployRequest struct {
	Source    string `json:"source"`    // compose and/or manifest YAML (multi-doc ok)
	Project   string `json:"project"`   // ownership label
	Namespace string `json:"namespace"` // target namespace
	Mode      string `json:"mode"`      // "" (auto) | compose | manifest
	Replicas  int    `json:"replicas"`
	PVCSize   string `json:"pvcSize"`
	Prune     *bool  `json:"prune"` // default true
	Wait      bool   `json:"wait"`
	ACMEEmail string `json:"acmeEmail"`
}

// parseDeployInput accepts either a JSON DeployRequest or a raw YAML body (with
// options in query params), returning the source bytes and an engine.Request.
func (s *Server) parseDeployInput(r *http.Request) ([]byte, engine.Request, error) {
	var dr DeployRequest
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := readJSON(r, &dr); err != nil {
			return nil, engine.Request{}, err
		}
	} else {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, engine.Request{}, err
		}
		q := r.URL.Query()
		dr = DeployRequest{
			Source:    string(body),
			Project:   q.Get("project"),
			Namespace: q.Get("namespace"),
			Mode:      q.Get("mode"),
			ACMEEmail: q.Get("acmeEmail"),
			Wait:      q.Get("wait") == "true",
		}
		if q.Get("prune") == "false" {
			f := false
			dr.Prune = &f
		}
		if n, err := strconv.Atoi(q.Get("replicas")); err == nil {
			dr.Replicas = n
		}
	}
	if dr.Project == "" {
		dr.Project = "default"
	}
	prune := true
	if dr.Prune != nil {
		prune = *dr.Prune
	}
	req := engine.Request{
		Project:     dr.Project,
		Namespace:   dr.Namespace,
		Mode:        dr.Mode,
		Replicas:    dr.Replicas,
		PVCSize:     dr.PVCSize,
		Kubeconfig:  s.cfg.Kubeconfig,
		Prune:       prune,
		Wait:        dr.Wait,
		ACMEEmail:   dr.ACMEEmail,
		AutoInstall: true,
	}
	return []byte(dr.Source), req, nil
}

// handleConvert renders the input to Kubernetes manifests without applying.
func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	source, req, err := s.parseDeployInput(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	objects, err := engine.BuildObjects([][]byte{source}, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := deploy.Render(objects)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"objects":   len(objects),
		"manifests": string(out),
	})
}

// handleDeploy converts + applies the input (auto-installing required plugins).
func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	source, req, err := s.parseDeployInput(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	applied, installed, err := engine.Deploy(r.Context(), [][]byte{source}, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"applied":   applied,
		"project":   req.Project,
		"installed": installed,
	})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	projects, err := a.ListProjects(r.Context(), r.URL.Query().Get("namespace"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"projects": projects})
}

func (s *Server) handleProjectPods(w http.ResponseWriter, r *http.Request) {
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	pods, err := a.ListProjectPods(r.Context(), r.PathValue("project"), r.URL.Query().Get("namespace"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"pods": pods})
}

func (s *Server) handleRemoveProject(w http.ResponseWriter, r *http.Request) {
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	removed, err := a.RemoveProject(r.Context(), r.PathValue("project"), namespaceOrDefault(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"removed": removed})
}

// ScaleRequest is the body for the scale endpoint.
type ScaleRequest struct {
	Replicas int32 `json:"replicas"`
}

func (s *Server) handleScale(w http.ResponseWriter, r *http.Request) {
	var body ScaleRequest
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	kind, err := a.Scale(r.Context(), namespaceOrDefault(r), r.PathValue("service"), body.Replicas)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"scaled": kind, "replicas": body.Replicas})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	msg, err := a.Rollback(r.Context(), namespaceOrDefault(r), r.PathValue("service"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"result": msg})
}

func (s *Server) handleListSecrets(w http.ResponseWriter, r *http.Request) {
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	secrets, err := a.ListSecrets(r.Context(), namespaceOrDefault(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"secrets": secrets})
}

// SecretRequest is the body for creating a secret.
type SecretRequest struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Data      map[string]string `json:"data"`
}

func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	var body SecretRequest
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Name == "" || len(body.Data) == 0 {
		writeErr(w, http.StatusBadRequest, "name and non-empty data are required")
		return
	}
	ns := body.Namespace
	if ns == "" {
		ns = "default"
	}
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	data := map[string][]byte{}
	for k, v := range body.Data {
		data[k] = []byte(v)
	}
	if err := a.ApplySecret(r.Context(), ns, body.Name, corev1.SecretTypeOpaque, data); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"name": body.Name, "namespace": ns, "keys": len(data)})
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	a, err := s.applier()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := a.DeleteSecret(r.Context(), namespaceOrDefault(r), r.PathValue("name")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListPlugins(w http.ResponseWriter, _ *http.Request) {
	type pluginInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Version     string   `json:"version"`
		Providers   []string `json:"providers,omitempty"`
		Installed   bool     `json:"installed"`
	}
	var out []pluginInfo
	for _, p := range plugin.List() {
		out = append(out, pluginInfo{
			Name: p.Name, Description: p.Description, Version: p.Version,
			Providers: p.Providers, Installed: plugin.Installed(p.Name),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"plugins": out})
}

// PluginRequest is the (optional) body for installing a plugin.
type PluginRequest struct {
	Email     string `json:"email"`
	Staging   bool   `json:"staging"`
	Provider  string `json:"provider"`
	Size      string `json:"size"`
	Replicas  int    `json:"replicas"`
	NFSServer string `json:"nfsServer"`
	NFSPath   string `json:"nfsPath"`
}

func (s *Server) handleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	var body PluginRequest
	if r.ContentLength > 0 {
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	name := r.PathValue("name")
	if _, ok := plugin.Get(name); !ok {
		writeErr(w, http.StatusNotFound, "unknown plugin: "+name)
		return
	}
	opts := plugin.Options{
		Kubeconfig: s.cfg.Kubeconfig,
		Email:      body.Email, Staging: body.Staging,
		Provider: body.Provider, Size: body.Size, Replicas: body.Replicas,
		NFSServer: body.NFSServer, NFSPath: body.NFSPath,
	}
	if err := plugin.Install(r.Context(), name, opts); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"installed": name})
}

func (s *Server) handleRemovePlugin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := plugin.Get(name); !ok {
		writeErr(w, http.StatusNotFound, "unknown plugin: "+name)
		return
	}
	var body PluginRequest
	if r.ContentLength > 0 {
		_ = readJSON(r, &body)
	}
	opts := plugin.Options{Kubeconfig: s.cfg.Kubeconfig, Provider: body.Provider, Replicas: body.Replicas}
	if err := plugin.Remove(r.Context(), name, opts); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCluster(w http.ResponseWriter, _ *http.Request) {
	st, err := cluster.Status("")
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":       st.State.Name,
		"running":    st.Running,
		"server":     st.State.ServerURL,
		"runtime":    st.State.Runtime,
		"kubeconfig": st.State.KubeconfigPath,
		"nodes":      st.Nodes,
	})
}

// namespaceOrDefault returns the ?namespace= query value or "default".
func namespaceOrDefault(r *http.Request) string {
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		return ns
	}
	return "default"
}

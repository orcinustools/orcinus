// Package plugin manages orcinus cluster add-ons (ingress, cert-manager,
// storage, …). A plugin is a curated set of manifests plus optional
// post-install objects; installing it reuses the orcinus deploy engine.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/orcinustools/orcinus/pkg/cluster"
	"github.com/orcinustools/orcinus/pkg/deploy"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Options are user-supplied install options (flags).
type Options struct {
	Kubeconfig string
	Email      string // cert-manager: ACME account email
	Staging    bool   // cert-manager: use Let's Encrypt staging
}

// WaitTarget is a Deployment to wait for before post-install steps.
type WaitTarget struct{ Namespace, Name string }

// Spec describes an installable plugin.
type Spec struct {
	Name        string
	Description string
	Manifests   []string // URLs applied in order
	WaitFor     []WaitTarget
	PostInstall func(o Options) ([]runtime.Object, error)
	Notes       string
}

// Registry is the built-in plugin catalog.
var Registry = map[string]Spec{
	"cert-manager": {
		Name:        "cert-manager",
		Description: "TLS certificate automation (+ a Let's Encrypt ClusterIssuer)",
		Manifests:   []string{"https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml"},
		WaitFor: []WaitTarget{
			{Namespace: "cert-manager", Name: "cert-manager-webhook"},
			{Namespace: "cert-manager", Name: "cert-manager"},
		},
		PostInstall: certManagerIssuer,
		Notes:       "Use x-orcinus-tls: letsencrypt on a service to get HTTPS.",
	},
	"ingress-nginx": {
		Name:        "ingress-nginx",
		Description: "NGINX ingress controller (ingress class: nginx)",
		Manifests:   []string{"https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/cloud/deploy.yaml"},
	},
	"metrics-server": {
		Name:        "metrics-server",
		Description: "Cluster metrics (kubectl top, HPA)",
		Manifests:   []string{"https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"},
	},
}

// List returns the catalog sorted by name.
func List() []Spec {
	out := make([]Spec, 0, len(Registry))
	for _, s := range Registry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Install applies a plugin's manifests, waits for readiness, applies any
// post-install objects, and records the plugin as installed.
func Install(ctx context.Context, name string, o Options) error {
	spec, ok := Registry[name]
	if !ok {
		return fmt.Errorf("unknown plugin %q (see `orcinus plugin list`)", name)
	}
	if name == "cert-manager" && o.Email == "" {
		return fmt.Errorf("cert-manager needs --email <you@example.com> for the ACME account")
	}

	cfg, err := deploy.LoadRESTConfig(o.Kubeconfig)
	if err != nil {
		return err
	}
	applier, err := deploy.NewApplier(cfg)
	if err != nil {
		return err
	}

	for _, url := range spec.Manifests {
		data, err := fetch(url)
		if err != nil {
			return err
		}
		objs, err := deploy.DecodeManifests(data)
		if err != nil {
			return fmt.Errorf("%s: %w", url, err)
		}
		if _, err := applier.Apply(ctx, objs, deploy.ApplyOptions{}); err != nil {
			return fmt.Errorf("apply %s: %w", spec.Name, err)
		}
	}

	for _, w := range spec.WaitFor {
		if err := applier.WaitForDeployment(ctx, w.Namespace, w.Name, 5*time.Minute); err != nil {
			return err
		}
	}

	if spec.PostInstall != nil {
		objs, err := spec.PostInstall(o)
		if err != nil {
			return err
		}
		// Fresh applier: its discovery RESTMapper must see CRDs installed above
		// (e.g. cert-manager's ClusterIssuer). The original mapper is stale.
		pa, err := deploy.NewApplier(cfg)
		if err != nil {
			return err
		}
		if _, err := pa.Apply(ctx, objs, deploy.ApplyOptions{}); err != nil {
			return fmt.Errorf("post-install %s: %w", spec.Name, err)
		}
	}

	return recordInstalled(spec.Name)
}

// certManagerIssuer builds a Let's Encrypt ClusterIssuer named "letsencrypt".
func certManagerIssuer(o Options) ([]runtime.Object, error) {
	server := "https://acme-v02.api.letsencrypt.org/directory"
	if o.Staging {
		server = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	issuer := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   map[string]interface{}{"name": "letsencrypt"},
		"spec": map[string]interface{}{
			"acme": map[string]interface{}{
				"server":              server,
				"email":               o.Email,
				"privateKeySecretRef": map[string]interface{}{"name": "letsencrypt-account"},
				"solvers": []interface{}{
					map[string]interface{}{
						"http01": map[string]interface{}{
							"ingress": map[string]interface{}{"class": "traefik"},
						},
					},
				},
			},
		},
	}}
	return []runtime.Object{issuer}, nil
}

// --- state ---

type state struct {
	Installed map[string]bool `json:"installed"`
}

func statePath() string { return filepath.Join(cluster.Dir(), "plugins.json") }

func loadState() state {
	s := state{Installed: map[string]bool{}}
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &s)
		if s.Installed == nil {
			s.Installed = map[string]bool{}
		}
	}
	return s
}

func recordInstalled(name string) error {
	s := loadState()
	s.Installed[name] = true
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(cluster.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(statePath(), b, 0o600)
}

// Installed reports whether a plugin is recorded as installed.
func Installed(name string) bool { return loadState().Installed[name] }

// --- helpers ---

func fetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %q: HTTP %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

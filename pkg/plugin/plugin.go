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

	// storage plugin options
	Provider  string // e.g. local-path | longhorn | nfs | minio | rook-ceph
	Size      string // volume/PVC size (e.g. 10Gi)
	Replicas  int    // minio: >1 = distributed; longhorn/rook: replica count
	NFSServer string // nfs provider
	NFSPath   string // nfs provider

	// rook-ceph tuning
	CephDeviceFilter  string // regex of devices to use (e.g. "^sd[b-d]")
	CephFailureDomain string // pool failure domain (host|osd|rack); default host
}

// WaitTarget is a Deployment to wait for before post-install steps.
type WaitTarget struct{ Namespace, Name string }

// built is the concrete result of resolving a plugin for given options.
type built struct {
	Namespace string           // if set, created first + used as the default ns for Manifests
	Manifests []string         // URLs applied in order
	Objects   []runtime.Object // inline objects applied after manifests
	WaitFor   []WaitTarget
	// PostObjects are applied last, with a fresh API discovery, so they may
	// reference CRDs installed by Manifests (e.g. a CephCluster).
	PostObjects []runtime.Object
}

// Spec describes an installable plugin.
type Spec struct {
	Name        string
	Description string
	Version     string           // pinned upstream version (informational)
	Providers   []string         // for `plugin info`; storage/ingress variants
	Namespace   string           // install namespace (created first)
	Manifests   []string         // static URLs (when Build is nil)
	Objects     []runtime.Object // static inline objects (when Build is nil)
	WaitFor     []WaitTarget
	PostInstall func(o Options) ([]runtime.Object, error)
	// Build resolves manifests/objects dynamically from options (overrides
	// Manifests). Used by plugins with providers, e.g. storage.
	Build func(o Options) (built, error)
	Notes string
}

// resolve returns the concrete install plan for a spec + options.
func resolve(spec Spec, o Options) (built, error) {
	if spec.Build != nil {
		return spec.Build(o)
	}
	return built{Namespace: spec.Namespace, Manifests: spec.Manifests, Objects: spec.Objects, WaitFor: spec.WaitFor}, nil
}

// Registry is the built-in plugin catalog.
var Registry = map[string]Spec{
	"cert-manager": {
		Name:        "cert-manager",
		Description: "TLS certificate automation (+ a Let's Encrypt ClusterIssuer)",
		Version:     "v1.16.2",
		Manifests:   []string{"https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml"},
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
		Version:     "controller-v1.11.3",
		Manifests:   []string{"https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/cloud/deploy.yaml"},
	},
	"metrics-server": {
		Name:        "metrics-server",
		Description: "Cluster metrics (kubectl top, HPA)",
		Version:     "v0.7.2",
		Manifests:   []string{"https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml"},
	},
	"argo-rollouts": {
		Name:        "argo-rollouts",
		Description: "Progressive delivery — canary & blue-green (Argo Rollouts)",
		Version:     "v1.7.2",
		Namespace:   "argo-rollouts",
		Manifests:   []string{"https://github.com/argoproj/argo-rollouts/releases/download/v1.7.2/install.yaml"},
		WaitFor:     []WaitTarget{{Namespace: "argo-rollouts", Name: "argo-rollouts"}},
		Notes:       "Use x-orcinus-rollout: canary|bluegreen on a service.",
	},
	"dashboard": {
		Name:        "dashboard",
		Description: "Kubernetes Dashboard (web UI)",
		Version:     "v2.7.0",
		Manifests:   []string{"https://raw.githubusercontent.com/kubernetes/dashboard/v2.7.0/aio/deploy/recommended.yaml"},
		WaitFor:     []WaitTarget{{Namespace: "kubernetes-dashboard", Name: "kubernetes-dashboard"}},
		Notes:       "Access via `kubectl -n kubernetes-dashboard port-forward svc/kubernetes-dashboard 8443:443`.",
	},
	"registry": {
		Name:        "registry",
		Description: "In-cluster image registry (registry:2)",
		Version:     "2",
		Namespace:   "orcinus-registry",
		Objects:     registryObjects(),
		WaitFor:     []WaitTarget{{Namespace: "orcinus-registry", Name: "registry"}},
		Notes:       "Reachable in-cluster at registry.orcinus-registry.svc:5000.",
	},
	"grafana": {
		Name:        "grafana",
		Description: "Grafana dashboards (point at Prometheus)",
		Version:     "11.2.0",
		Namespace:   "orcinus-monitoring",
		Objects:     grafanaObjects(),
		WaitFor:     []WaitTarget{{Namespace: "orcinus-monitoring", Name: "grafana"}},
		Notes:       "Admin: admin/admin (change it). Add Prometheus data source manually.",
	},
	"monitoring": {
		Name:        "monitoring",
		Description: "Prometheus Operator (CRDs + operator for Prometheus/Alertmanager)",
		Manifests:   []string{"https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.76.2/bundle.yaml"},
		WaitFor:     []WaitTarget{{Namespace: "default", Name: "prometheus-operator"}},
		Notes:       "Then create Prometheus/Alertmanager custom resources.",
	},
	"storage": {
		Name:        "storage",
		Description: "Storage backends (block, file/NFS, object)",
		Providers:   []string{"local-path", "longhorn", "nfs", "minio", "rook-ceph"},
		Build:       buildStorage,
		Notes:       "Pick with --provider. nfs needs --nfs-server/--nfs-path; minio is S3 object storage; rook-ceph is full distributed storage.",
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

	plan, err := resolve(spec, o)
	if err != nil {
		return err
	}

	cfg, err := deploy.LoadRESTConfig(o.Kubeconfig)
	if err != nil {
		return err
	}
	applier, err := deploy.NewApplier(cfg)
	if err != nil {
		return err
	}

	// Create the install namespace first (many manifests assume it exists).
	if plan.Namespace != "" {
		if _, err := applier.Apply(ctx, []runtime.Object{namespaceObj(plan.Namespace)}, deploy.ApplyOptions{}); err != nil {
			return err
		}
	}
	for _, url := range plan.Manifests {
		data, err := fetch(url)
		if err != nil {
			return err
		}
		objs, err := deploy.DecodeManifests(data)
		if err != nil {
			return fmt.Errorf("%s: %w", url, err)
		}
		if _, err := applier.Apply(ctx, objs, deploy.ApplyOptions{DefaultNamespace: plan.Namespace}); err != nil {
			return fmt.Errorf("apply %s: %w", spec.Name, err)
		}
	}
	if len(plan.Objects) > 0 {
		if _, err := applier.Apply(ctx, plan.Objects, deploy.ApplyOptions{}); err != nil {
			return fmt.Errorf("apply %s: %w", spec.Name, err)
		}
	}

	for _, w := range plan.WaitFor {
		if err := applier.WaitForDeployment(ctx, w.Namespace, w.Name, 5*time.Minute); err != nil {
			return err
		}
	}

	// Post-install objects need a fresh discovery RESTMapper so CRDs installed
	// above (cert-manager's ClusterIssuer, Rook's CephCluster) are visible.
	var post []runtime.Object
	if spec.PostInstall != nil {
		objs, err := spec.PostInstall(o)
		if err != nil {
			return err
		}
		post = append(post, objs...)
	}
	post = append(post, plan.PostObjects...)
	if len(post) > 0 {
		pa, err := deploy.NewApplier(cfg)
		if err != nil {
			return err
		}
		if _, err := pa.Apply(ctx, post, deploy.ApplyOptions{}); err != nil {
			return fmt.Errorf("post-install %s: %w", spec.Name, err)
		}
	}

	return recordInstalled(spec.Name)
}

// Get returns a plugin spec by name.
func Get(name string) (Spec, bool) {
	s, ok := Registry[name]
	return s, ok
}

// Profiles bundle a set of plugins installed together.
var Profiles = map[string][]string{
	"web":           {"cert-manager", "ingress-nginx"},
	"observability": {"metrics-server", "monitoring", "grafana"},
}

// InstallProfile installs every plugin in a named profile, in order.
func InstallProfile(ctx context.Context, name string, o Options) error {
	names, ok := Profiles[name]
	if !ok {
		avail := make([]string, 0, len(Profiles))
		for k := range Profiles {
			avail = append(avail, k)
		}
		sort.Strings(avail)
		return fmt.Errorf("unknown profile %q (available: %v)", name, avail)
	}
	for _, n := range names {
		if err := Install(ctx, n, o); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
	}
	return nil
}

// Remove deletes what a plugin installed (post-install objects, then manifests)
// and unrecords it. Manifests are re-fetched to know what to delete.
func Remove(ctx context.Context, name string, o Options) error {
	spec, ok := Registry[name]
	if !ok {
		return fmt.Errorf("unknown plugin %q", name)
	}
	plan, err := resolve(spec, o)
	if err != nil {
		return err
	}
	cfg, err := deploy.LoadRESTConfig(o.Kubeconfig)
	if err != nil {
		return err
	}
	applier, err := deploy.NewApplier(cfg)
	if err != nil {
		return err
	}

	if spec.PostInstall != nil {
		if objs, err := spec.PostInstall(o); err == nil {
			_ = applier.DeleteObjects(ctx, objs)
		}
	}
	_ = applier.DeleteObjects(ctx, plan.PostObjects)
	_ = applier.DeleteObjects(ctx, plan.Objects)
	for _, url := range plan.Manifests {
		data, err := fetch(url)
		if err != nil {
			return err
		}
		objs, err := deploy.DecodeManifests(data)
		if err != nil {
			return err
		}
		if err := applier.DeleteObjects(ctx, objs); err != nil {
			return err
		}
	}
	// Namespace cleanup: remove the install namespace we created (if any).
	if plan.Namespace != "" {
		_ = applier.DeleteObjects(ctx, []runtime.Object{namespaceObj(plan.Namespace)})
	}
	return unrecord(name)
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

func unrecord(name string) error {
	s := loadState()
	delete(s.Installed, name)
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(cluster.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(statePath(), b, 0o600)
}

// --- helpers ---

func namespaceObj(name string) runtime.Object {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]interface{}{"name": name},
	}}
}

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

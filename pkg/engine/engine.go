// Package engine holds the shared deploy pipeline (detect → convert → auto-install
// → server-side apply) used by both the CLI (`orcinus deploy`) and the HTTP API
// (`orcinus api`). Keeping it here avoids duplicating the orchestration and keeps
// the two front-ends behaviorally identical.
package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/orcinustools/orcinus/pkg/compose"
	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/detect"
	"github.com/orcinustools/orcinus/pkg/plugin"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// Request configures a deploy/convert. It mirrors the `orcinus deploy` flags.
type Request struct {
	Project     string
	Namespace   string
	Mode        string // "" (auto) | "compose" | "manifest"
	Replicas    int
	PVCSize     string
	Kubeconfig  string
	Prune       bool
	Wait        bool
	WaitTimeout time.Duration
	ACMEEmail   string // enables auto-installing cert-manager for x-orcinus-tls
	AutoInstall bool   // auto-install cert-manager / argo-rollouts when required
}

// BuildObjects splits every source into YAML documents, classifies each as
// compose or manifest, and converts the compose docs through the forked kompose
// engine. The returned objects are ready to render or apply.
func BuildObjects(sources [][]byte, req Request) ([]runtime.Object, error) {
	mode, err := detect.ParseMode(req.Mode)
	if err != nil {
		return nil, err
	}
	if req.Replicas <= 0 {
		req.Replicas = 1
	}
	if req.PVCSize == "" {
		req.PVCSize = "1Gi"
	}

	var composeDocs [][]byte
	var manifestObjs []runtime.Object
	for _, raw := range sources {
		docs, err := detect.SplitDocuments(raw)
		if err != nil {
			return nil, err
		}
		for _, doc := range docs {
			kind, err := detect.Classify(doc, mode)
			if err != nil {
				return nil, err
			}
			switch kind {
			case detect.KindCompose:
				composeDocs = append(composeDocs, doc)
			case detect.KindManifest:
				obj, err := decodeManifest(doc)
				if err != nil {
					return nil, err
				}
				manifestObjs = append(manifestObjs, obj)
			}
		}
	}

	objects := manifestObjs
	if len(composeDocs) > 0 {
		tmpDir, err := os.MkdirTemp("", "orcinus-engine-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpDir)
		var files []string
		for i, doc := range composeDocs {
			p := filepath.Join(tmpDir, fmt.Sprintf("compose-%02d.yml", i))
			if err := os.WriteFile(p, doc, 0o600); err != nil {
				return nil, err
			}
			files = append(files, p)
		}
		converted, err := compose.Convert(compose.Options{
			Files:       files,
			ProjectName: req.Project,
			Namespace:   req.Namespace,
			Replicas:    req.Replicas,
			PVCSize:     req.PVCSize,
		})
		if err != nil {
			return nil, err
		}
		objects = append(objects, converted...)
	}

	if len(objects) == 0 {
		return nil, fmt.Errorf("no compose services or manifests found in input")
	}
	return objects, nil
}

// AutoInstall installs cert-manager and/or argo-rollouts when the objects require
// them and they are not already present. Returns the plugins it installed.
func AutoInstall(ctx context.Context, objects []runtime.Object, req Request) ([]string, error) {
	var installed []string
	if NeedsCertManager(objects) && !plugin.Installed("cert-manager") {
		if req.ACMEEmail == "" {
			return nil, fmt.Errorf("x-orcinus-tls needs cert-manager; install it first or provide an ACME email")
		}
		if err := plugin.Install(ctx, "cert-manager", plugin.Options{Kubeconfig: req.Kubeconfig, Email: req.ACMEEmail}); err != nil {
			return installed, err
		}
		installed = append(installed, "cert-manager")
	}
	if NeedsArgoRollouts(objects) && !plugin.Installed("argo-rollouts") {
		if err := plugin.Install(ctx, "argo-rollouts", plugin.Options{Kubeconfig: req.Kubeconfig}); err != nil {
			return installed, err
		}
		installed = append(installed, "argo-rollouts")
	}
	return installed, nil
}

// Apply server-side-applies the objects (+ prune + optional wait) and returns the
// number of objects applied.
func Apply(ctx context.Context, objects []runtime.Object, req Request) (int, error) {
	cfg, err := deploy.LoadRESTConfig(req.Kubeconfig)
	if err != nil {
		return 0, err
	}
	applier, err := deploy.NewApplier(cfg)
	if err != nil {
		return 0, err
	}
	applied, err := applier.Apply(ctx, objects, deploy.ApplyOptions{
		Project:          req.Project,
		DefaultNamespace: req.Namespace,
		Prune:            req.Prune,
		Wait:             req.Wait,
		WaitTimeout:      req.WaitTimeout,
	})
	if err != nil {
		return 0, err
	}
	return len(applied), nil
}

// Deploy is the full pipeline: build objects, auto-install required plugins (when
// req.AutoInstall), and apply. Returns objects applied and plugins installed.
func Deploy(ctx context.Context, sources [][]byte, req Request) (applied int, installed []string, err error) {
	objects, err := BuildObjects(sources, req)
	if err != nil {
		return 0, nil, err
	}
	if req.AutoInstall {
		installed, err = AutoInstall(ctx, objects, req)
		if err != nil {
			return 0, installed, err
		}
	}
	applied, err = Apply(ctx, objects, req)
	return applied, installed, err
}

// NeedsCertManager reports whether any object requests a cert-manager issuer.
func NeedsCertManager(objects []runtime.Object) bool {
	for _, o := range objects {
		acc, err := meta.Accessor(o)
		if err != nil {
			continue
		}
		if _, ok := acc.GetAnnotations()["cert-manager.io/cluster-issuer"]; ok {
			return true
		}
	}
	return false
}

// NeedsArgoRollouts reports whether any object is an Argo Rollout.
func NeedsArgoRollouts(objects []runtime.Object) bool {
	for _, o := range objects {
		gvk := o.GetObjectKind().GroupVersionKind()
		if gvk.Group == "argoproj.io" && gvk.Kind == "Rollout" {
			return true
		}
	}
	return false
}

// decodeManifest turns a raw k8s YAML document into an unstructured object.
func decodeManifest(doc []byte) (runtime.Object, error) {
	m := map[string]interface{}{}
	if err := yaml.Unmarshal(doc, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &unstructured.Unstructured{Object: m}, nil
}

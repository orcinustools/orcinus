package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orcinustools/orcinus/pkg/compose"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
)

// fieldManager identifies orcinus in Kubernetes server-side apply.
const fieldManager = "orcinus"

// Applier applies converted objects to a cluster via server-side apply, and
// prunes owned resources that are no longer present (ARCHITECTURE.md §4).
type Applier struct {
	dyn       dynamic.Interface
	clientset kubernetes.Interface
	mapper    meta.RESTMapper
}

// AppliedRef records an object that was applied (used for prune bookkeeping).
type AppliedRef struct {
	GVR        schema.GroupVersionResource
	GVK        schema.GroupVersionKind
	Namespace  string
	Name       string
	Namespaced bool
}

// ApplyOptions controls apply/prune/wait behavior.
type ApplyOptions struct {
	Project          string
	DefaultNamespace string
	Prune            bool
	Wait             bool
	WaitTimeout      time.Duration
}

// LoadRESTConfig resolves a *rest.Config from, in order: an explicit path,
// $KUBECONFIG, the orcinus-managed cluster kubeconfig (~/.orcinus/kubeconfig,
// written by `orcinus init`), then ~/.kube/config.
func LoadRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig == "" {
		if p := OrcinusKubeconfigPath(); p != "" && fileExists(p) {
			kubeconfig = p
		}
	}
	if kubeconfig == "" {
		if home, err := os.UserHomeDir(); err == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}
	if kubeconfig == "" {
		return nil, fmt.Errorf("no kubeconfig found (run `orcinus cluster init`, pass --kubeconfig, or set KUBECONFIG)")
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

// OrcinusKubeconfigPath is where `orcinus init` writes the cluster kubeconfig.
func OrcinusKubeconfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".orcinus", "kubeconfig")
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// NewApplier builds an Applier from a REST config.
func NewApplier(cfg *rest.Config) (*Applier, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return nil, fmt.Errorf("discover API resources: %w", err)
	}
	return &Applier{dyn: dyn, clientset: cs, mapper: restmapper.NewDiscoveryRESTMapper(groups)}, nil
}

// Apply server-side-applies every object, then optionally prunes and waits.
func (a *Applier) Apply(ctx context.Context, objects []runtime.Object, opts ApplyOptions) ([]AppliedRef, error) {
	ns := opts.DefaultNamespace
	if ns == "" {
		ns = "default"
	}

	var applied []AppliedRef
	for _, obj := range objects {
		u, err := toUnstructured(obj)
		if err != nil {
			return nil, err
		}
		cleanForApply(u)

		gvk := u.GroupVersionKind()
		mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return nil, fmt.Errorf("map %s: %w", gvk, err)
		}
		namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace

		var ri dynamic.ResourceInterface
		objNS := u.GetNamespace()
		if namespaced {
			if objNS == "" {
				objNS = ns
				u.SetNamespace(objNS)
			}
			ri = a.dyn.Resource(mapping.Resource).Namespace(objNS)
		} else {
			ri = a.dyn.Resource(mapping.Resource)
		}

		if _, err := ri.Apply(ctx, u.GetName(), u, metav1.ApplyOptions{FieldManager: fieldManager, Force: true}); err != nil {
			return nil, fmt.Errorf("apply %s/%s: %w", strings.ToLower(gvk.Kind), u.GetName(), err)
		}
		applied = append(applied, AppliedRef{
			GVR: mapping.Resource, GVK: gvk, Namespace: objNS, Name: u.GetName(), Namespaced: namespaced,
		})
	}

	if opts.Prune {
		if err := a.prune(ctx, applied, opts.Project, ns); err != nil {
			return applied, fmt.Errorf("prune: %w", err)
		}
	}
	if opts.Wait {
		if err := a.wait(ctx, applied, opts.WaitTimeout); err != nil {
			return applied, err
		}
	}
	return applied, nil
}

// prunableGVRs is the fixed set of resource types orcinus owns and may prune.
var prunableGVRs = []schema.GroupVersionResource{
	{Group: "apps", Version: "v1", Resource: "deployments"},
	{Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "apps", Version: "v1", Resource: "daemonsets"},
	{Group: "", Version: "v1", Resource: "services"},
	{Group: "", Version: "v1", Resource: "configmaps"},
	{Group: "", Version: "v1", Resource: "secrets"},
	{Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
	{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
}

// prune deletes owned resources (managed-by=orcinus, project=<project>) that are
// not part of the just-applied set. Requires a project scope for safety.
func (a *Applier) prune(ctx context.Context, applied []AppliedRef, project, ns string) error {
	if project == "" {
		return nil // never prune without a project scope
	}
	keep := map[string]bool{}
	for _, r := range applied {
		keep[key(r.GVR, r.Namespace, r.Name)] = true
	}
	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, compose.LabelProject, project)

	for _, gvr := range prunableGVRs {
		list, err := a.dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			continue // resource type may not exist on this cluster
		}
		for i := range list.Items {
			item := &list.Items[i]
			if keep[key(gvr, item.GetNamespace(), item.GetName())] {
				continue
			}
			_ = a.dyn.Resource(gvr).Namespace(item.GetNamespace()).
				Delete(ctx, item.GetName(), metav1.DeleteOptions{})
		}
	}
	return nil
}

// RemoveProject deletes every owned resource of a project (backs `orcinus rm`).
func (a *Applier) RemoveProject(ctx context.Context, project, ns string) (int, error) {
	if project == "" {
		return 0, fmt.Errorf("project name is required")
	}
	if ns == "" {
		ns = "default"
	}
	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, compose.LabelProject, project)

	deleted := 0
	for _, gvr := range prunableGVRs {
		list, err := a.dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			continue
		}
		for i := range list.Items {
			item := &list.Items[i]
			if err := a.dyn.Resource(gvr).Namespace(item.GetNamespace()).
				Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err == nil {
				deleted++
			}
		}
	}
	return deleted, nil
}

// wait blocks until all applied workloads report ready, or the timeout elapses.
func (a *Applier) wait(ctx context.Context, applied []AppliedRef, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for _, r := range applied {
		if !isWorkload(r.GVK.Kind) {
			continue
		}
		for {
			ready, err := a.workloadReady(ctx, r)
			if err == nil && ready {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out waiting for %s/%s to become ready", strings.ToLower(r.GVK.Kind), r.Name)
			}
			time.Sleep(3 * time.Second)
		}
	}
	return nil
}

func (a *Applier) workloadReady(ctx context.Context, r AppliedRef) (bool, error) {
	u, err := a.dyn.Resource(r.GVR).Namespace(r.Namespace).Get(ctx, r.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	status, _, _ := unstructured.NestedMap(u.Object, "status")
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	switch r.GVK.Kind {
	case "Deployment", "StatefulSet":
		want := nestedInt(spec, "replicas", 1)
		return nestedInt(status, "readyReplicas", 0) >= want, nil
	case "DaemonSet":
		want := nestedInt(status, "desiredNumberScheduled", 0)
		return want > 0 && nestedInt(status, "numberReady", 0) >= want, nil
	}
	return true, nil
}

func isWorkload(kind string) bool {
	return kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet"
}

// --- helpers ---

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u, nil
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: m}, nil
}

// cleanForApply strips fields that break/pollute server-side apply.
func cleanForApply(u *unstructured.Unstructured) {
	unstructured.RemoveNestedField(u.Object, "status")
	unstructured.RemoveNestedField(u.Object, "metadata", "creationTimestamp")
	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(u.Object, "metadata", "resourceVersion")
	unstructured.RemoveNestedField(u.Object, "metadata", "uid")
}

func key(gvr schema.GroupVersionResource, ns, name string) string {
	return gvr.String() + "|" + ns + "|" + name
}

func nestedInt(m map[string]interface{}, field string, def int64) int64 {
	if m == nil {
		return def
	}
	if v, ok, _ := unstructured.NestedInt64(m, field); ok {
		return v
	}
	return def
}

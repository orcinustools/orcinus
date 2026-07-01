package deploy

import (
	"context"
	"fmt"
	"sort"

	"github.com/biznetgio/orcinus/pkg/compose"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ProjectSummary aggregates the orcinus-managed workloads of one project.
type ProjectSummary struct {
	Name       string
	Namespaces []string
	Workloads  int
}

// workloadGVRs are the controller types counted by `orcinus ls`.
var workloadGVRs = []schema.GroupVersionResource{
	{Group: "apps", Version: "v1", Resource: "deployments"},
	{Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "apps", Version: "v1", Resource: "daemonsets"},
}

// ListProjects returns orcinus-managed projects. If namespace is empty it looks
// cluster-wide; otherwise it is scoped to that namespace.
func (a *Applier) ListProjects(ctx context.Context, namespace string) ([]ProjectSummary, error) {
	selector := fmt.Sprintf("%s=%s", compose.LabelManagedBy, compose.ManagedByValue)
	opts := metav1.ListOptions{LabelSelector: selector}

	agg := map[string]*ProjectSummary{}
	nsSeen := map[string]map[string]bool{}

	for _, gvr := range workloadGVRs {
		items, err := a.listWorkloads(ctx, gvr, namespace, opts)
		if err != nil {
			continue // type may not be listable on this cluster; skip
		}
		for i := range items {
			it := &items[i]
			proj := it.GetLabels()[compose.LabelProject]
			if proj == "" {
				proj = "<none>"
			}
			s := agg[proj]
			if s == nil {
				s = &ProjectSummary{Name: proj}
				agg[proj] = s
				nsSeen[proj] = map[string]bool{}
			}
			s.Workloads++
			if ns := it.GetNamespace(); !nsSeen[proj][ns] {
				nsSeen[proj][ns] = true
				s.Namespaces = append(s.Namespaces, ns)
			}
		}
	}

	out := make([]ProjectSummary, 0, len(agg))
	for _, s := range agg {
		sort.Strings(s.Namespaces)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (a *Applier) listWorkloads(ctx context.Context, gvr schema.GroupVersionResource, namespace string, opts metav1.ListOptions) ([]unstructured.Unstructured, error) {
	if namespace == "" {
		ul, err := a.dyn.Resource(gvr).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		return ul.Items, nil
	}
	ul, err := a.dyn.Resource(gvr).Namespace(namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	return ul.Items, nil
}

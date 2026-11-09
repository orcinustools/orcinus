package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/orcinustools/orcinus/pkg/deploy"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const kyvernoVersion = "v1.19.2"

// NamespaceLabel marks the namespaces a plugin installed into (value: the
// plugin). Image signing lets them run upstream images unsigned.
const NamespaceLabel = "orcinus.io/plugin"

// Namespaces that are never labeled: "default" holds user workloads, and the
// system ones are exempt from signing on their own.
var unlabeled = map[string]bool{"default": true, "kube-system": true, "kube-public": true, "kube-node-lease": true}

// footprintOf finds the namespaces objs live in, and the images of the
// workloads among them that run in "default".
func footprintOf(objs []runtime.Object, defaultNS string) footprint {
	ns, imgs := map[string]bool{}, map[string]bool{}
	if defaultNS != "" {
		ns[defaultNS] = true
	}
	for _, o := range objs {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			continue
		}
		u := &unstructured.Unstructured{Object: m}
		if u.GetKind() == "Namespace" {
			ns[u.GetName()] = true
			continue
		}
		n := u.GetNamespace()
		if n == "" && namespaced(u.GetKind()) {
			n = defaultNS
			if n == "" {
				n = "default"
			}
		}
		if n == "" {
			continue
		}
		ns[n] = true
		if n == "default" {
			for _, list := range deploy.ContainerLists(u) {
				for _, c := range list {
					if img, _ := c.(map[string]any)["image"].(string); img != "" {
						imgs[img] = true
					}
				}
			}
		}
	}
	var fp footprint
	for n := range ns {
		if !unlabeled[n] {
			fp.Namespaces = append(fp.Namespaces, n)
		}
	}
	for i := range imgs {
		fp.DefaultImages = append(fp.DefaultImages, i)
	}
	sort.Strings(fp.Namespaces)
	sort.Strings(fp.DefaultImages)
	return fp
}

// namespaced guesses whether a kind lives in a namespace, for objects whose
// manifest leaves it to the default. Cluster-scoped kinds plugins ship:
func namespaced(kind string) bool {
	switch kind {
	case "CustomResourceDefinition", "ClusterRole", "ClusterRoleBinding", "PriorityClass", "StorageClass",
		"ValidatingWebhookConfiguration", "MutatingWebhookConfiguration", "APIService", "RuntimeClass",
		"ClusterIssuer", "CSIDriver", "IngressClass", "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding":
		return false
	}
	return true
}

func labelNamespaces(ctx context.Context, kubeconfig, plugin string, namespaces []string) error {
	if len(namespaces) == 0 {
		return nil
	}
	cfg, err := deploy.LoadRESTConfig(kubeconfig)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"labels": map[string]string{NamespaceLabel: plugin}}})
	for _, n := range namespaces {
		_, err := cs.CoreV1().Namespaces().Patch(ctx, n, types.MergePatchType, patch, metav1.PatchOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("label namespace %s: %w", n, err)
		}
	}
	return nil
}

// LabelInstalled labels the namespaces of every installed plugin (for plugins
// installed before footprints were recorded, it works them out from the
// plugin's manifests with default options) and returns the images plugins run
// in "default".
func LabelInstalled(ctx context.Context, kubeconfig string) ([]string, error) {
	s := loadState()
	var images []string
	for name := range s.Installed {
		fp, ok := s.Footprints[name]
		if !ok {
			spec, known := Registry[name]
			if !known {
				continue
			}
			plan, err := resolve(spec, Options{Kubeconfig: kubeconfig})
			if err != nil {
				return nil, fmt.Errorf("plugin %s: %w", name, err)
			}
			objs := append([]runtime.Object{}, plan.Objects...)
			for _, url := range plan.Manifests {
				data, err := fetch(url)
				if err != nil {
					return nil, err
				}
				o, err := deploy.DecodeManifests(data)
				if err != nil {
					return nil, err
				}
				objs = append(objs, o...)
			}
			fp = footprintOf(append(objs, plan.PostObjects...), plan.Namespace)
			s.Footprints[name] = fp
			if err := saveState(s); err != nil {
				return nil, err
			}
		}
		if err := labelNamespaces(ctx, kubeconfig, name, fp.Namespaces); err != nil {
			return nil, err
		}
		images = append(images, fp.DefaultImages...)
	}
	sort.Strings(images)
	return images, nil
}

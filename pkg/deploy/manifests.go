package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/orcinustools/orcinus/pkg/detect"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// DecodeManifests splits a (multi-document) YAML stream into unstructured objects.
func DecodeManifests(data []byte) ([]runtime.Object, error) {
	docs, err := detect.SplitDocuments(data)
	if err != nil {
		return nil, err
	}
	var out []runtime.Object
	for _, doc := range docs {
		m := map[string]interface{}{}
		if err := yaml.Unmarshal(doc, &m); err != nil {
			return nil, fmt.Errorf("parse manifest: %w", err)
		}
		if len(m) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: m})
	}
	return out, nil
}

// DeleteObjects deletes the given objects (used by `orcinus plugin remove`).
// It deletes in reverse order and ignores not-found / unmappable kinds (e.g. a
// CRD already removed by a cascading delete).
func (a *Applier) DeleteObjects(ctx context.Context, objects []runtime.Object) error {
	for i := len(objects) - 1; i >= 0; i-- {
		u, err := toUnstructured(objects[i])
		if err != nil {
			continue
		}
		gvk := u.GroupVersionKind()
		mapping, err := a.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			continue
		}
		ns := u.GetNamespace()
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace && ns == "" {
			ns = "default"
		}
		_ = a.dyn.Resource(mapping.Resource).Namespace(ns).
			Delete(ctx, u.GetName(), metav1.DeleteOptions{})
	}
	return nil
}

// DeploymentReady is a non-blocking readiness check for a Deployment.
func (a *Applier) DeploymentReady(ctx context.Context, namespace, name string) bool {
	d, err := a.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	return d.Status.ReadyReplicas >= want
}

// WaitForDeployment blocks until a Deployment reports its desired replicas ready.
func (a *Applier) WaitForDeployment(ctx context.Context, namespace, name string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		d, err := a.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if d.Status.ReadyReplicas >= want {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for deployment %s/%s", namespace, name)
		}
		time.Sleep(3 * time.Second)
	}
}

package deploy

import (
	"context"
	"fmt"
	"time"

	"github.com/orcinustools/orcinus/pkg/detect"

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

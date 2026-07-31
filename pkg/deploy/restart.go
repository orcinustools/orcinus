package deploy

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// restartedAt is the pod-template annotation `kubectl rollout restart` stamps.
// Changing its value rewrites the template, which makes the controller replace
// every pod through its normal rolling update — no downtime for a service with
// spare replicas, and no change to the workload's spec.
const restartedAt = "kubectl.kubernetes.io/restartedAt"

// Restart rolls every pod of a Deployment, StatefulSet, DaemonSet, or Argo
// Rollout named `name`. It returns the kind that was restarted.
func (a *Applier) Restart(ctx context.Context, namespace, name string) (string, error) {
	patch := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`,
		restartedAt, time.Now().UTC().Format(time.RFC3339)))
	apps := a.clientset.AppsV1()

	if _, err := apps.Deployments(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		_, err := apps.Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		return kindOrErr("Deployment", err)
	}
	if _, err := apps.StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		_, err := apps.StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		return kindOrErr("StatefulSet", err)
	}
	if _, err := apps.DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		_, err := apps.DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		return kindOrErr("DaemonSet", err)
	}
	// Rollout is a CRD, so it has no strategic-merge schema — a JSON merge
	// patch sets the same annotation.
	if _, err := a.dyn.Resource(rolloutGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		_, err := a.dyn.Resource(rolloutGVR).Namespace(namespace).
			Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		return kindOrErr("Rollout", err)
	}
	return "", fmt.Errorf("no Deployment, StatefulSet, DaemonSet, or Rollout %q in namespace %q", name, namespace)
}

func kindOrErr(kind string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return kind, nil
}

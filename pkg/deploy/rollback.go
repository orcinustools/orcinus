package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var rolloutGVR = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}

// Rollback reverts a Deployment, StatefulSet, or Argo Rollout to its previous
// revision. Returns the kind that was rolled back.
func (a *Applier) Rollback(ctx context.Context, namespace, name string) (string, error) {
	if d, err := a.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return "Deployment", a.rollbackDeployment(ctx, namespace, d)
	}
	if s, err := a.clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return "StatefulSet", a.rollbackStatefulSet(ctx, namespace, s)
	}
	if _, err := a.dyn.Resource(rolloutGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return "Rollout", a.rollbackRollout(ctx, namespace, name)
	}
	return "", fmt.Errorf("no Deployment, StatefulSet, or Rollout %q in namespace %q", name, namespace)
}

// previousRSTemplate returns the pod template of the newest ReplicaSet whose
// revision is below currentRev (i.e. the previous revision).
func (a *Applier) previousRSTemplate(ctx context.Context, namespace string, selector map[string]string, revAnno, hashLabel string, currentRev int64) (*corev1.PodTemplateSpec, error) {
	sel := labels.SelectorFromSet(selector).String()
	list, err := a.clientset.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, err
	}
	var best *appsv1.ReplicaSet
	var bestRev int64 = -1
	for i := range list.Items {
		rs := &list.Items[i]
		rev, _ := strconv.ParseInt(rs.Annotations[revAnno], 10, 64)
		if rev < currentRev && rev > bestRev {
			bestRev, best = rev, rs
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no previous revision to roll back to")
	}
	tmpl := best.Spec.Template.DeepCopy()
	delete(tmpl.Labels, hashLabel)
	return tmpl, nil
}

func (a *Applier) rollbackDeployment(ctx context.Context, namespace string, d *appsv1.Deployment) error {
	cur, _ := strconv.ParseInt(d.Annotations["deployment.kubernetes.io/revision"], 10, 64)
	tmpl, err := a.previousRSTemplate(ctx, namespace, d.Spec.Selector.MatchLabels,
		"deployment.kubernetes.io/revision", "pod-template-hash", cur)
	if err != nil {
		return err
	}
	d.Spec.Template = *tmpl
	_, err = a.clientset.AppsV1().Deployments(namespace).Update(ctx, d, metav1.UpdateOptions{})
	return err
}

func (a *Applier) rollbackRollout(ctx context.Context, namespace, name string) error {
	ro, err := a.dyn.Resource(rolloutGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	cur, _ := strconv.ParseInt(ro.GetAnnotations()["rollout.argoproj.io/revision"], 10, 64)
	selector, _, _ := unstructured.NestedStringMap(ro.Object, "spec", "selector", "matchLabels")
	tmpl, err := a.previousRSTemplate(ctx, namespace, selector,
		"rollout.argoproj.io/revision", "rollouts-pod-template-hash", cur)
	if err != nil {
		return err
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tmpl)
	if err != nil {
		return err
	}
	unstructured.RemoveNestedField(m, "metadata", "creationTimestamp")
	if err := unstructured.SetNestedMap(ro.Object, m, "spec", "template"); err != nil {
		return err
	}
	_, err = a.dyn.Resource(rolloutGVR).Namespace(namespace).Update(ctx, ro, metav1.UpdateOptions{})
	return err
}

func (a *Applier) rollbackStatefulSet(ctx context.Context, namespace string, s *appsv1.StatefulSet) error {
	sel := labels.SelectorFromSet(s.Spec.Selector.MatchLabels).String()
	revs, err := a.clientset.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return err
	}
	if len(revs.Items) < 2 {
		return fmt.Errorf("no previous revision to roll back to")
	}
	sort.Slice(revs.Items, func(i, j int) bool { return revs.Items[i].Revision > revs.Items[j].Revision })
	prev := revs.Items[1] // second-newest = previous

	var patch map[string]interface{}
	if err := json.Unmarshal(prev.Data.Raw, &patch); err != nil {
		return fmt.Errorf("parse ControllerRevision: %w", err)
	}
	tmplMap, ok, _ := unstructured.NestedMap(patch, "spec", "template")
	if !ok {
		return fmt.Errorf("previous revision has no pod template")
	}
	var tmpl corev1.PodTemplateSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(tmplMap, &tmpl); err != nil {
		return fmt.Errorf("decode previous template: %w", err)
	}
	s.Spec.Template = tmpl
	_, err = a.clientset.AppsV1().StatefulSets(namespace).Update(ctx, s, metav1.UpdateOptions{})
	return err
}

package deploy

import (
	"context"
	"fmt"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/orcinustools/orcinus/pkg/compose"
)

// Scale sets the replica count of a Deployment or StatefulSet named `name`.
// It returns the kind that was scaled.
func (a *Applier) Scale(ctx context.Context, namespace, name string, replicas int32) (string, error) {
	if d, err := a.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		d.Spec.Replicas = &replicas
		if _, err := a.clientset.AppsV1().Deployments(namespace).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
			return "", err
		}
		return "Deployment", nil
	}
	if s, err := a.clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		s.Spec.Replicas = &replicas
		if _, err := a.clientset.AppsV1().StatefulSets(namespace).Update(ctx, s, metav1.UpdateOptions{}); err != nil {
			return "", err
		}
		return "StatefulSet", nil
	}
	return "", fmt.Errorf("no Deployment or StatefulSet %q in namespace %q", name, namespace)
}

// AutoscaleSpec describes a HorizontalPodAutoscaler to create/update.
type AutoscaleSpec struct {
	Min    int32
	Max    int32
	CPU    int32 // target average CPU utilization %; 0 = unset
	Memory int32 // target average memory utilization %; 0 = unset
}

// Autoscale creates or updates an HPA for the Deployment/StatefulSet `name`.
func (a *Applier) Autoscale(ctx context.Context, namespace, name string, spec AutoscaleSpec) error {
	kind := ""
	if _, err := a.clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		kind = "Deployment"
	} else if _, err := a.clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		kind = "StatefulSet"
	} else {
		return fmt.Errorf("no Deployment or StatefulSet %q in namespace %q", name, namespace)
	}

	hpa := BuildHPA(namespace, name, kind, spec)
	hpas := a.clientset.AutoscalingV2().HorizontalPodAutoscalers(namespace)
	if existing, err := hpas.Get(ctx, name, metav1.GetOptions{}); err == nil {
		hpa.ResourceVersion = existing.ResourceVersion
		_, err = hpas.Update(ctx, hpa, metav1.UpdateOptions{})
		return err
	}
	_, err := hpas.Create(ctx, hpa, metav1.CreateOptions{})
	return err
}

// BuildHPA constructs an autoscaling/v2 HPA targeting a workload. Exposed so the
// compose layer can generate the same object from x-orcinus-autoscale-*.
func BuildHPA(namespace, name, targetKind string, s AutoscaleSpec) *autoscalingv2.HorizontalPodAutoscaler {
	min := s.Min
	if min < 1 {
		min = 1
	}
	if s.CPU == 0 && s.Memory == 0 {
		s.CPU = 80 // sensible default
	}
	var metrics []autoscalingv2.MetricSpec
	if s.CPU > 0 {
		metrics = append(metrics, resourceMetric("cpu", s.CPU))
	}
	if s.Memory > 0 {
		metrics = append(metrics, resourceMetric("memory", s.Memory))
	}
	return &autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{compose.LabelManagedBy: compose.ManagedByValue},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1", Kind: targetKind, Name: name,
			},
			MinReplicas: &min,
			MaxReplicas: s.Max,
			Metrics:     metrics,
		},
	}
}

func resourceMetric(name string, target int32) autoscalingv2.MetricSpec {
	t := target
	return autoscalingv2.MetricSpec{
		Type: autoscalingv2.ResourceMetricSourceType,
		Resource: &autoscalingv2.ResourceMetricSource{
			Name: corev1.ResourceName(name),
			Target: autoscalingv2.MetricTarget{
				Type:               autoscalingv2.UtilizationMetricType,
				AverageUtilization: &t,
			},
		},
	}
}

package deploy

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// restartApplier wires both fakes: Restart falls through the apps/v1 kinds to
// the Rollout CRD, so the dynamic client has to be there even when unused.
func restartApplier(objs ...runtime.Object) (*Applier, *k8sfake.Clientset) {
	cs := k8sfake.NewSimpleClientset(objs...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rolloutGVR: "RolloutList"})
	return &Applier{clientset: cs, dyn: dyn}, cs
}

func deployment(name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "redis:7"}}},
			},
		},
	}
}

func statefulSet(name string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "redis:7"}}},
			},
		},
	}
}

func daemonSet(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "redis:7"}}},
			},
		},
	}
}

// Restart stamps the pod template of whichever controller owns the service, and
// reports the kind it found.
func TestRestartStampsPodTemplate(t *testing.T) {
	a, cs := restartApplier(deployment("web"), statefulSet("redis"), daemonSet("agent"))
	ctx := context.Background()

	for _, tc := range []struct {
		name, wantKind string
		annotations    func() map[string]string
	}{
		{"web", "Deployment", func() map[string]string {
			d, _ := cs.AppsV1().Deployments(testNS).Get(ctx, "web", metav1.GetOptions{})
			return d.Spec.Template.Annotations
		}},
		{"redis", "StatefulSet", func() map[string]string {
			s, _ := cs.AppsV1().StatefulSets(testNS).Get(ctx, "redis", metav1.GetOptions{})
			return s.Spec.Template.Annotations
		}},
		{"agent", "DaemonSet", func() map[string]string {
			d, _ := cs.AppsV1().DaemonSets(testNS).Get(ctx, "agent", metav1.GetOptions{})
			return d.Spec.Template.Annotations
		}},
	} {
		kind, err := a.Restart(ctx, testNS, tc.name)
		if err != nil {
			t.Fatalf("restart %s: %v", tc.name, err)
		}
		if kind != tc.wantKind {
			t.Errorf("restart %s: kind = %q, want %q", tc.name, kind, tc.wantKind)
		}
		if got := tc.annotations()[restartedAt]; got == "" {
			t.Errorf("restart %s: %s annotation not set", tc.name, restartedAt)
		}
	}
}

// Restarting must not change the workload itself — only the pod template stamp.
func TestRestartLeavesSpecAlone(t *testing.T) {
	want := int32(3)
	d := deployment("web")
	d.Spec.Replicas = &want
	a, cs := restartApplier(d)

	if _, err := a.Restart(context.Background(), testNS, "web"); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got, _ := cs.AppsV1().Deployments(testNS).Get(context.Background(), "web", metav1.GetOptions{})
	if got.Spec.Replicas == nil || *got.Spec.Replicas != want {
		t.Errorf("replicas changed: got %v, want %d", got.Spec.Replicas, want)
	}
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "redis:7" {
		t.Errorf("image changed to %q", img)
	}
}

func TestRestartUnknownService(t *testing.T) {
	a, _ := restartApplier()
	if _, err := a.Restart(context.Background(), testNS, "nope"); err == nil {
		t.Fatal("restarting a service that does not exist should fail")
	}
}

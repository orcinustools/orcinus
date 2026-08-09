package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/orcinustools/orcinus/pkg/compose"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// execPod builds a pod that looks like one orcinus deployed: managed-by and
// service labels, the given phase, and one container per name.
func execPod(name, service string, phase corev1.PodPhase, containers ...string) *corev1.Pod {
	if len(containers) == 0 {
		containers = []string{"app"}
	}
	specs := make([]corev1.Container, 0, len(containers))
	for _, c := range containers {
		specs = append(specs, corev1.Container{Name: c, Image: "busybox"})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels: map[string]string{
				compose.LabelManagedBy: compose.ManagedByValue,
				serviceLabel:           service,
				compose.LabelProject:   "shop",
			},
		},
		Spec:   corev1.PodSpec{Containers: specs},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func execApplier(objs ...runtime.Object) *Applier {
	return &Applier{clientset: k8sfake.NewSimpleClientset(objs...)}
}

// A service name resolves to one of its running pods, deterministically.
func TestResolveExecPodByService(t *testing.T) {
	a := execApplier(
		execPod("web-2", "web", corev1.PodRunning),
		execPod("web-1", "web", corev1.PodRunning),
	)
	var notes []string
	pod, err := a.resolveExecPod(context.Background(), ExecOptions{
		Target: "web", Namespace: testNS, Notify: func(m string) { notes = append(notes, m) },
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if pod.Name != "web-1" {
		t.Errorf("pod = %s, want web-1 (lowest name, for a stable pick)", pod.Name)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "web-1") {
		t.Errorf("want a note naming the pod that was picked, got %v", notes)
	}
}

// Pods that are not running are not somewhere a command can run: say so, and
// say what they are doing instead.
func TestResolveExecPodNoneRunning(t *testing.T) {
	a := execApplier(execPod("web-1", "web", corev1.PodPending))
	_, err := a.resolveExecPod(context.Background(), ExecOptions{Target: "web", Namespace: testNS})
	if err == nil {
		t.Fatal("want an error when no pod is running")
	}
	for _, want := range []string{"no running pod", "web-1", "Pending"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// --project narrows the lookup, so a service name shared by two apps is not
// ambiguous.
func TestResolveExecPodScopedByProject(t *testing.T) {
	other := execPod("web-other", "web", corev1.PodRunning)
	other.Labels[compose.LabelProject] = "blog"
	a := execApplier(other, execPod("web-shop", "web", corev1.PodRunning))

	pod, err := a.resolveExecPod(context.Background(), ExecOptions{
		Target: "web", Project: "blog", Namespace: testNS,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if pod.Name != "web-other" {
		t.Errorf("pod = %s, want web-other", pod.Name)
	}
}

// A name that matches no service is tried as a pod name, so a line copied out
// of `orcinus ps` works without a flag.
func TestResolveExecPodFallsBackToPodName(t *testing.T) {
	a := execApplier(execPod("web-5d9f-abcde", "web", corev1.PodRunning))
	pod, err := a.resolveExecPod(context.Background(), ExecOptions{
		Target: "web-5d9f-abcde", Namespace: testNS,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if pod.Name != "web-5d9f-abcde" {
		t.Errorf("pod = %s, want the pod named by the target", pod.Name)
	}
}

func TestResolveExecPodUnknownTarget(t *testing.T) {
	a := execApplier()
	_, err := a.resolveExecPod(context.Background(), ExecOptions{Target: "nope", Namespace: testNS})
	if err == nil || !strings.Contains(err.Error(), `no service or pod "nope"`) {
		t.Errorf("error = %v, want it to name the missing target", err)
	}
}

// The pod-name fallback still respects --project: a pod from another app is not
// a match, and the error says which scope came up empty.
func TestResolveExecPodFallbackRespectsProject(t *testing.T) {
	a := execApplier(execPod("web-1", "web", corev1.PodRunning)) // project "shop"

	if _, err := a.resolveExecPod(context.Background(), ExecOptions{
		Target: "web-1", Project: "shop", Namespace: testNS,
	}); err != nil {
		t.Fatalf("a pod in the named project should resolve: %v", err)
	}

	_, err := a.resolveExecPod(context.Background(), ExecOptions{
		Target: "web-1", Project: "blog", Namespace: testNS,
	})
	if err == nil {
		t.Fatal("want an error for a pod outside the named project")
	}
	for _, want := range []string{`"web-1"`, `project "blog"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// --pod skips service resolution, including for pods orcinus does not manage.
func TestResolveExecPodExplicit(t *testing.T) {
	unmanaged := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "loose", Namespace: testNS},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	a := execApplier(unmanaged)
	pod, err := a.resolveExecPod(context.Background(), ExecOptions{Pod: "loose", Namespace: testNS})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if pod.Name != "loose" {
		t.Errorf("pod = %s, want loose", pod.Name)
	}

	if _, err := a.resolveExecPod(context.Background(), ExecOptions{Pod: "ghost", Namespace: testNS}); err == nil ||
		!strings.Contains(err.Error(), `no pod "ghost"`) {
		t.Errorf("error = %v, want it to name the missing pod", err)
	}
}

func TestPickExecContainer(t *testing.T) {
	single := execPod("web-1", "web", corev1.PodRunning)
	multi := execPod("web-2", "web", corev1.PodRunning, "app", "sidecar")
	hinted := execPod("web-3", "web", corev1.PodRunning, "app", "sidecar")
	hinted.Annotations = map[string]string{defaultContainerAnnotation: "sidecar"}
	staleHint := execPod("web-4", "web", corev1.PodRunning, "app", "sidecar")
	staleHint.Annotations = map[string]string{defaultContainerAnnotation: "gone"}

	for _, tc := range []struct {
		name     string
		pod      *corev1.Pod
		want     string
		wantName string
		notes    int
	}{
		{name: "only container", pod: single, want: "", wantName: "app"},
		{name: "explicit", pod: multi, want: "sidecar", wantName: "sidecar"},
		{name: "first of several", pod: multi, want: "", wantName: "app", notes: 1},
		{name: "annotation hint", pod: hinted, want: "", wantName: "sidecar"},
		{name: "hint names no container", pod: staleHint, want: "", wantName: "app", notes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var notes int
			got, err := pickExecContainer(tc.pod, tc.want, func(string) { notes++ })
			if err != nil {
				t.Fatalf("pick: %v", err)
			}
			if got != tc.wantName {
				t.Errorf("container = %s, want %s", got, tc.wantName)
			}
			if notes != tc.notes {
				t.Errorf("notes = %d, want %d", notes, tc.notes)
			}
		})
	}
}

func TestPickExecContainerUnknown(t *testing.T) {
	pod := execPod("web-1", "web", corev1.PodRunning, "app", "sidecar")
	_, err := pickExecContainer(pod, "nope", nil)
	if err == nil {
		t.Fatal("want an error for a container that is not there")
	}
	for _, want := range []string{`"nope"`, "app", "sidecar"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q — it should list what is available", err, want)
		}
	}
}

// Exec validates its inputs before reaching for the cluster, so a bad call
// fails with a clear message rather than a nil-pointer panic.
func TestExecRejectsBadOptions(t *testing.T) {
	a := execApplier(execPod("web-1", "web", corev1.PodRunning))
	for _, tc := range []struct {
		name string
		opts ExecOptions
		want string
	}{
		{"no command", ExecOptions{Target: "web"}, "command to run is required"},
		{"tty without stdin", ExecOptions{Target: "web", Command: []string{"sh"}, TTY: true}, "TTY needs stdin"},
		{"no connection", ExecOptions{Target: "web", Command: []string{"sh"}}, "needs a cluster connection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := a.Exec(context.Background(), tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

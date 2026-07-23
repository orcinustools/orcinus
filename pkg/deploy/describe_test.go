package deploy

import (
	"bytes"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "staging",
			Labels:    map[string]string{"app": "web", "io.kompose.service": "web"},
		},
		Spec: corev1.PodSpec{
			NodeName:     "node-1",
			NodeSelector: map[string]string{"disktype": "ssd"},
			Containers: []corev1.Container{{
				Name:  "web",
				Image: "nginx:1.27",
				Ports: []corev1.ContainerPort{{ContainerPort: 80, Protocol: corev1.ProtocolTCP}},
				Env: []corev1.EnvVar{
					{Name: "LOG_LEVEL", Value: "info"},
					{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"},
							Key:                  "token",
						},
					}},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "web-pvc"},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase:    corev1.PodRunning,
			PodIP:    "10.0.0.5",
			QOSClass: corev1.PodQOSBestEffort,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "web",
				Ready:        true,
				RestartCount: 2,
				Image:        "nginx:1.27",
				ContainerID:  "containerd://deadbeef",
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
	events := []corev1.Event{{
		Type:    "Normal",
		Reason:  "Started",
		Message: "Started container web",
		Source:  corev1.EventSource{Component: "kubelet"},
	}}

	var buf bytes.Buffer
	if err := renderPod(&buf, pod, events); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"Name:", "web-abc123",
		"Namespace:", "staging",
		"Node:", "node-1",
		"Status:", "Running",
		"IP:", "10.0.0.5",
		"Containers:",
		"Image:", "nginx:1.27",
		"Restart Count:", "2",
		"Ports:", "80/TCP",
		"LOG_LEVEL", "info",
		"TOKEN", "<set to the key 'token' in secret 'app-secret'>",
		"Mounts:", "/data from data (ro)",
		"Conditions:", "Ready",
		"Volumes:", "PersistentVolumeClaim", "web-pvc",
		"QoS Class:", "BestEffort",
		"Node-Selectors:", "disktype=ssd",
		"Events:", "Started", "Started container web",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered pod missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderPodNoEvents(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	var buf bytes.Buffer
	if err := renderPod(&buf, pod, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Events:") || !strings.Contains(buf.String(), "<none>") {
		t.Errorf("expected 'Events: <none>', got:\n%s", buf.String())
	}
}

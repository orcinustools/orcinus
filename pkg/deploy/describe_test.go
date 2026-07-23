package deploy

import (
	"bytes"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ptrInt32(v int32) *int32 { return &v }

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

func TestRenderDeployment(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web",
			Namespace: "default",
			Labels:    map[string]string{"io.kompose.service": "web"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrInt32(3),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"io.kompose.service": "web"}},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"io.kompose.service": "web"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Image: "nginx:1.27"}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          3,
			UpdatedReplicas:   3,
			AvailableReplicas: 3,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"},
			},
		},
	}
	var buf bytes.Buffer
	if err := renderDeployment(&buf, d, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "web",
		"Selector:", "io.kompose.service=web",
		"Replicas:", "3 desired", "3 available",
		"StrategyType:", "RollingUpdate",
		"Pod Template:", "nginx:1.27",
		"Conditions:", "Available", "MinimumReplicasAvailable",
		"Events:", "<none>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered deployment missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderNode(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: map[string]string{"node-role.kubernetes.io/control-plane": "", "zone": "east"},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints:        []corev1.Taint{{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue, Reason: "KubeletReady"}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
			NodeInfo: corev1.NodeSystemInfo{
				Architecture:            "arm64",
				OperatingSystem:         "linux",
				OSImage:                 "Alpine",
				KernelVersion:           "6.1",
				ContainerRuntimeVersion: "containerd://1.7",
				KubeletVersion:          "v1.31.0",
			},
		},
	}
	var buf bytes.Buffer
	if err := renderNode(&buf, node, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Name:", "node-1",
		"Roles:", "control-plane",
		"Unschedulable:", "true",
		"Taints:", "NoSchedule",
		"Conditions:", "Ready", "KubeletReady",
		"Addresses:", "10.0.0.1",
		"System Info:", "arm64", "v1.31.0", "containerd://1.7",
		"Events:", "<none>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered node missing %q\n---\n%s", want, out)
		}
	}
}

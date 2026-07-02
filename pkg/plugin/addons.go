package plugin

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func int32p(n int32) *int32 { return &n }

// simpleWorkload builds ns + PVC + Deployment + Service for a one-container addon.
func simpleWorkload(namespace, name, image string, port int32, size, mountPath string, env []corev1.EnvVar) []runtime.Object {
	labels := map[string]string{"app": name}
	pvc := &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
		},
	}
	dep := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32p(1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:         name,
						Image:        image,
						Env:          env,
						Ports:        []corev1.ContainerPort{{ContainerPort: port}},
						VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: mountPath}},
					}},
					Volumes: []corev1.Volume{{
						Name:         "data",
						VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}},
					}},
				},
			},
		},
	}
	svc := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Port: port, TargetPort: intstr.FromInt(int(port))}},
		},
	}
	return []runtime.Object{ns(namespace), pvc, dep, svc}
}

func registryObjects() []runtime.Object {
	return simpleWorkload("orcinus-registry", "registry", "registry:2", 5000, "10Gi", "/var/lib/registry", nil)
}

func grafanaObjects() []runtime.Object {
	return simpleWorkload("orcinus-monitoring", "grafana", "grafana/grafana:11.2.0", 3000, "2Gi", "/var/lib/grafana",
		[]corev1.EnvVar{{Name: "GF_SECURITY_ADMIN_PASSWORD", Value: "admin"}})
}

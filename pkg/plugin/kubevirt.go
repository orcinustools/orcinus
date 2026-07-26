package plugin

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	kubeVirtVersion = "v1.8.4"
	cdiVersion      = "v1.65.0"
	kubeVirtNS      = "kubevirt"
	cdiNS           = "cdi"
)

func kubeVirtURL(file string) string {
	return fmt.Sprintf("https://github.com/kubevirt/kubevirt/releases/download/%s/%s", kubeVirtVersion, file)
}

func cdiURL(file string) string {
	return fmt.Sprintf("https://github.com/kubevirt/containerized-data-importer/releases/download/%s/%s", cdiVersion, file)
}

// buildKubeVirt resolves the kubevirt plugin: the virt-operator manifest plus a
// KubeVirt CR built from the options. The CR is a PostObject because it needs
// the kubevirts.kubevirt.io CRD the operator manifest installs. With --cdi the
// Containerized Data Importer (disk images from URLs/registries) comes along.
func buildKubeVirt(o Options) (built, error) {
	b := built{
		Manifests:   []string{kubeVirtURL("kubevirt-operator.yaml")},
		WaitFor:     []WaitTarget{{Namespace: kubeVirtNS, Name: "virt-operator"}},
		PostObjects: []runtime.Object{kubeVirtCR(o)},
	}
	if o.CDI {
		b.Manifests = append(b.Manifests, cdiURL("cdi-operator.yaml"))
		b.WaitFor = append(b.WaitFor, WaitTarget{Namespace: cdiNS, Name: "cdi-operator"})
		b.PostObjects = append(b.PostObjects, cdiCR())
	}
	return b, nil
}

// kubeVirtCR is the KubeVirt custom resource the operator reconciles into the
// actual control plane (virt-api, virt-controller, virt-handler).
func kubeVirtCR(o Options) runtime.Object {
	dev := map[string]interface{}{"featureGates": []interface{}{}}
	if o.Emulation {
		// No /dev/kvm on the node (the common case for a containerized k3s
		// cluster) — run VMs under QEMU software emulation instead.
		dev["useEmulation"] = true
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "KubeVirt",
		"metadata":   map[string]interface{}{"name": "kubevirt", "namespace": kubeVirtNS},
		"spec": map[string]interface{}{
			"certificateRotateStrategy": map[string]interface{}{},
			"customizeComponents":       map[string]interface{}{},
			"imagePullPolicy":           "IfNotPresent",
			"workloadUpdateStrategy":    map[string]interface{}{},
			"configuration": map[string]interface{}{
				"developerConfiguration": dev,
				"imagePullPolicy":        "IfNotPresent",
			},
		},
	}}
}

// cdiCR is the cluster-scoped CDI resource (DataVolumes: import/upload/clone
// disk images into PVCs).
func cdiCR() runtime.Object {
	linux := map[string]interface{}{"nodeSelector": map[string]interface{}{"kubernetes.io/os": "linux"}}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "CDI",
		"metadata":   map[string]interface{}{"name": "cdi"},
		"spec": map[string]interface{}{
			"imagePullPolicy": "IfNotPresent",
			"config": map[string]interface{}{
				"featureGates": []interface{}{"HonorWaitForFirstConsumer", "WebhookPvcRendering"},
			},
			"infra":    linux,
			"workload": linux,
		},
	}}
}

package compose

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// convertVMs replaces the Deployment of each x-orcinus-vm service with a KubeVirt
// VirtualMachine. The Service/Ingress the fork generated stay as they are: the VM
// keeps the Deployment's pod labels, so the existing selector finds the VM's pod
// and a VM is reachable exactly like a container.
func convertVMs(objects []runtime.Object, cfgs map[string]vmCfg) ([]runtime.Object, error) {
	if len(cfgs) == 0 {
		return objects, nil
	}
	converted := map[string]bool{}
	out := make([]runtime.Object, 0, len(objects)+len(cfgs))
	for _, o := range objects {
		dep, ok := o.(*appsv1.Deployment)
		if !ok {
			out = append(out, o)
			continue
		}
		cfg, want := cfgs[dep.Name]
		if !want {
			out = append(out, o)
			continue
		}
		vm, dv, err := deploymentToVM(dep, cfg)
		if err != nil {
			return nil, err
		}
		out = append(out, vm)
		if dv != nil {
			out = append(out, dv)
		}
		converted[dep.Name] = true
	}
	for name := range cfgs {
		if !converted[name] {
			return nil, fmt.Errorf("%s on service %q: no Deployment to convert — remove %s or drop the controller override",
				extVM, name, extController)
		}
	}
	return out, nil
}

// deploymentToVM builds a KubeVirt VirtualMachine from the Deployment the fork
// generated for a service. When cfg.DiskSize is set it also returns a standalone
// DataVolume (CDI) holding the imported image; otherwise the VM boots from an
// ephemeral containerDisk.
func deploymentToVM(dep *appsv1.Deployment, cfg vmCfg) (runtime.Object, runtime.Object, error) {
	if dep.Spec.Replicas != nil && *dep.Spec.Replicas > 1 {
		return nil, nil, fmt.Errorf("%s on service %q: a VM is a single instance, so replicas must be 1 (got %d)",
			extVM, dep.Name, *dep.Spec.Replicas)
	}
	pod := dep.Spec.Template.Spec
	if len(pod.Containers) == 0 {
		return nil, nil, fmt.Errorf("%s on service %q: no image to boot from", extVM, dep.Name)
	}
	c := pod.Containers[0]
	if c.Image == "" {
		return nil, nil, fmt.Errorf("%s on service %q: `image:` is required (a distro disk, e.g. quay.io/containerdisks/ubuntu:24.04)", extVM, dep.Name)
	}

	// Pod labels drive the Service selector — reuse the Deployment's so every
	// Service/Ingress already generated keeps pointing at this workload.
	labels := dep.Spec.Template.Labels
	if len(labels) == 0 && dep.Spec.Selector != nil {
		labels = dep.Spec.Selector.MatchLabels
	}

	volumes := []interface{}{}
	var dataVolume runtime.Object
	if cfg.DiskSize == "" {
		volumes = append(volumes, map[string]interface{}{
			"name":          "rootdisk",
			"containerDisk": map[string]interface{}{"image": c.Image},
		})
	} else {
		dvName := dep.Name + "-root"
		volumes = append(volumes, map[string]interface{}{
			"name":       "rootdisk",
			"dataVolume": map[string]interface{}{"name": dvName},
		})
		dataVolume = vmDataVolume(dvName, dep.Namespace, dep.Labels, c.Image, cfg.DiskSize)
	}
	volumes = append(volumes, map[string]interface{}{
		"name":             "cloudinit",
		"cloudInitNoCloud": map[string]interface{}{"userData": vmUserData(dep.Name, cfg)},
	})

	vmiSpec := map[string]interface{}{
		"domain": map[string]interface{}{
			"cpu":    map[string]interface{}{"cores": int64(vmCores(c.Resources))},
			"memory": map[string]interface{}{"guest": vmMemory(c.Resources)},
			"devices": map[string]interface{}{
				"rng": map[string]interface{}{}, // entropy, so cloud-init doesn't stall
				"disks": []interface{}{
					map[string]interface{}{"name": "rootdisk", "disk": map[string]interface{}{"bus": "virtio"}},
					map[string]interface{}{"name": "cloudinit", "disk": map[string]interface{}{"bus": "virtio"}},
				},
				"interfaces": []interface{}{
					map[string]interface{}{"name": "default", "masquerade": map[string]interface{}{}},
				},
			},
		},
		"networks": []interface{}{
			map[string]interface{}{"name": "default", "pod": map[string]interface{}{}},
		},
		"volumes": volumes,
	}
	// Placement carries over: x-orcinus-node-selector and deploy.placement were
	// already applied to the Deployment, and a VMI takes the same fields.
	if len(pod.NodeSelector) > 0 {
		vmiSpec["nodeSelector"] = toStringMapIface(pod.NodeSelector)
	}
	if pod.Affinity != nil {
		if m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod.Affinity); err == nil {
			vmiSpec["affinity"] = m
		}
	}
	if creds := vmAccessCredentials(cfg); creds != nil {
		vmiSpec["accessCredentials"] = creds
	}

	vm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata":   vmMeta(dep.Name, dep.Namespace, dep.Labels),
		"spec": map[string]interface{}{
			"runStrategy": cfg.RunStrategy,
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{"labels": toStringMapIface(labels)},
				"spec":     vmiSpec,
			},
		},
	}}
	return vm, dataVolume, nil
}

// vmMeta builds object metadata, leaving the namespace key out when empty so the
// applier's default namespace applies (and rendered YAML stays clean).
func vmMeta(name, namespace string, labels map[string]string) map[string]interface{} {
	md := map[string]interface{}{"name": name, "labels": toStringMapIface(labels)}
	if namespace != "" {
		md["namespace"] = namespace
	}
	return md
}

// vmDataVolume imports the disk image into a PVC once (CDI), so guest writes
// survive a restart — unlike a containerDisk's throwaway overlay.
func vmDataVolume(name, namespace string, labels map[string]string, image, size string) runtime.Object {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataVolume",
		"metadata":   vmMeta(name, namespace, labels),
		"spec": map[string]interface{}{
			"source": map[string]interface{}{
				"registry": map[string]interface{}{"url": "docker://" + image},
			},
			"storage": map[string]interface{}{
				"accessModes": []interface{}{"ReadWriteOnce"},
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{"storage": size},
				},
			},
		},
	}}
}

// vmAccessCredentials wires an SSH public key from a Secret into the guest.
// Without explicit users the key goes to the image's default account (noCloud
// propagation); naming users needs qemu-guest-agent running in the guest.
func vmAccessCredentials(cfg vmCfg) []interface{} {
	if cfg.SSHSecret == "" {
		return nil
	}
	propagation := map[string]interface{}{"noCloud": map[string]interface{}{}}
	if len(cfg.SSHUsers) > 0 {
		users := make([]interface{}, 0, len(cfg.SSHUsers))
		for _, u := range cfg.SSHUsers {
			users = append(users, u)
		}
		propagation = map[string]interface{}{"qemuGuestAgent": map[string]interface{}{"users": users}}
	}
	return []interface{}{map[string]interface{}{
		"sshPublicKey": map[string]interface{}{
			"source":            map[string]interface{}{"secret": map[string]interface{}{"secretName": cfg.SSHSecret}},
			"propagationMethod": propagation,
		},
	}}
}

// vmUserData returns the cloud-init user data: the service's own cloud-config if
// it gave one, otherwise a minimal document that just names the host.
func vmUserData(name string, cfg vmCfg) string {
	if cfg.CloudInit != "" {
		return cfg.CloudInit
	}
	return "#cloud-config\nhostname: " + name + "\n"
}

// vmCores maps a CPU limit/request to whole vCPUs (KubeVirt cores are integral),
// rounding up and defaulting to 1.
func vmCores(r corev1.ResourceRequirements) int {
	q, ok := r.Limits[corev1.ResourceCPU]
	if !ok {
		q, ok = r.Requests[corev1.ResourceCPU]
	}
	if !ok {
		return 1
	}
	milli := q.MilliValue()
	if milli <= 0 {
		return 1
	}
	cores := int((milli + 999) / 1000)
	if cores < 1 {
		return 1
	}
	return cores
}

// vmMemory maps a memory limit/request to the guest memory, defaulting to 1Gi
// (cloud images generally do not boot in less).
func vmMemory(r corev1.ResourceRequirements) string {
	q, ok := r.Limits[corev1.ResourceMemory]
	if !ok {
		q, ok = r.Requests[corev1.ResourceMemory]
	}
	if !ok || q.IsZero() {
		return "1Gi"
	}
	// A compose `memory: 2G` arrives as a raw byte count (2147483648). Render whole
	// Mi/Gi so the generated manifest reads like something a human wrote.
	if b, ok := q.AsInt64(); ok && b > 0 {
		const mi = int64(1024 * 1024)
		switch {
		case b%(1024*mi) == 0:
			return fmt.Sprintf("%dGi", b/(1024*mi))
		case b%mi == 0:
			return fmt.Sprintf("%dMi", b/mi)
		}
	}
	return q.String()
}

package deploy

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// ContainerLists returns the container arrays (initContainers, containers) of
// a Pod, a pod-template workload (Deployment, StatefulSet, DaemonSet, Job, …)
// or a CronJob, as stored in obj: editing the maps in them edits obj.
func ContainerLists(obj *unstructured.Unstructured) [][]any {
	spec := []string{"spec", "template", "spec"}
	switch obj.GetKind() {
	case "Pod":
		spec = []string{"spec"}
	case "CronJob":
		spec = []string{"spec", "jobTemplate", "spec", "template", "spec"}
	}
	var out [][]any
	for _, field := range []string{"initContainers", "containers"} {
		if l, ok, _ := unstructured.NestedFieldNoCopy(obj.Object, append(spec, field)...); ok {
			if arr, ok := l.([]any); ok {
				out = append(out, arr)
			}
		}
	}
	return out
}

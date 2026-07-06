package deploy

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeInfo is a compact view of a cluster node for `orcinus node ls`.
type NodeInfo struct {
	Name    string
	Status  string
	Roles   string
	Version string
}

// ListNodes returns the cluster's nodes (backs `orcinus node ls`).
func (a *Applier) ListNodes(ctx context.Context) ([]NodeInfo, error) {
	list, err := a.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]NodeInfo, 0, len(list.Items))
	for i := range list.Items {
		n := &list.Items[i]
		status := "NotReady"
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				status = "Ready"
			}
		}
		var roles []string
		for k := range n.Labels {
			if r := strings.TrimPrefix(k, "node-role.kubernetes.io/"); r != k && r != "" {
				roles = append(roles, r)
			}
		}
		sort.Strings(roles)
		rolesStr := "<none>"
		if len(roles) > 0 {
			rolesStr = strings.Join(roles, ",")
		}
		out = append(out, NodeInfo{
			Name: n.Name, Status: status, Roles: rolesStr, Version: n.Status.NodeInfo.KubeletVersion,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LabelNode sets and/or removes labels on a node (backs `orcinus node label`),
// like `docker node update --label-add/--label-rm`.
func (a *Applier) LabelNode(ctx context.Context, name string, set map[string]string, remove []string) error {
	nodes := a.clientset.CoreV1().Nodes()
	node, err := nodes.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	for k, v := range set {
		node.Labels[k] = v
	}
	for _, k := range remove {
		delete(node.Labels, k)
	}
	_, err = nodes.Update(ctx, node, metav1.UpdateOptions{})
	return err
}

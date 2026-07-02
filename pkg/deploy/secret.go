package deploy

import (
	"context"
	"sort"

	"github.com/orcinustools/orcinus/pkg/compose"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretInfo is a compact view of a Secret for `orcinus secret ls`.
type SecretInfo struct {
	Name      string
	Type      string
	Keys      int
	ManagedBy bool
}

// ApplySecret creates or updates a Secret (idempotent), labeled managed-by=orcinus.
func (a *Applier) ApplySecret(ctx context.Context, namespace, name string, typ corev1.SecretType, data map[string][]byte) error {
	secrets := a.clientset.CoreV1().Secrets(namespace)
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{compose.LabelManagedBy: compose.ManagedByValue},
		},
		Type: typ,
		Data: data,
	}
	if existing, err := secrets.Get(ctx, name, metav1.GetOptions{}); err == nil {
		sec.ResourceVersion = existing.ResourceVersion
		_, err = secrets.Update(ctx, sec, metav1.UpdateOptions{})
		return err
	}
	_, err := secrets.Create(ctx, sec, metav1.CreateOptions{})
	return err
}

// ListSecrets returns the Secrets in a namespace.
func (a *Applier) ListSecrets(ctx context.Context, namespace string) ([]SecretInfo, error) {
	list, err := a.clientset.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]SecretInfo, 0, len(list.Items))
	for i := range list.Items {
		s := &list.Items[i]
		out = append(out, SecretInfo{
			Name:      s.Name,
			Type:      string(s.Type),
			Keys:      len(s.Data),
			ManagedBy: s.Labels[compose.LabelManagedBy] == compose.ManagedByValue,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// DeleteSecret removes a Secret.
func (a *Applier) DeleteSecret(ctx context.Context, namespace, name string) error {
	return a.clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

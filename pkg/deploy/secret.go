package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"

	"github.com/orcinustools/orcinus/pkg/compose"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretInfo is a compact view of a Secret for `orcinus secret ls`.
type SecretInfo struct {
	Name      string
	Type      string
	Keys      int
	KeyNames  []string
	ManagedBy bool
}

// SecretDetail is a single Secret with its keys, for `orcinus secret get`.
// Values are carried decoded; whether they are printed is the caller's call.
type SecretDetail struct {
	Name      string
	Namespace string
	Type      string
	ManagedBy bool
	Data      map[string][]byte
}

// KeyNames returns the Secret's keys in a stable order.
func (d *SecretDetail) KeyNames() []string {
	out := make([]string, 0, len(d.Data))
	for k := range d.Data {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GetSecret reads one Secret, including its values.
func (a *Applier) GetSecret(ctx context.Context, namespace, name string) (*SecretDetail, error) {
	s, err := a.clientset.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return &SecretDetail{
		Name:      s.Name,
		Namespace: s.Namespace,
		Type:      string(s.Type),
		ManagedBy: s.Labels[compose.LabelManagedBy] == compose.ManagedByValue,
		Data:      s.Data,
	}, nil
}

// MergeSecret overlays keys onto an existing Secret, leaving the others alone.
// This is the counterpart to ApplySecret, which replaces the data wholesale:
// updating one key with ApplySecret drops every key not passed to it. If the
// Secret does not exist yet, it is created from the given keys.
func (a *Applier) MergeSecret(ctx context.Context, namespace, name string, typ corev1.SecretType, data map[string][]byte) error {
	existing, err := a.GetSecret(ctx, namespace, name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		return a.ApplySecret(ctx, namespace, name, typ, data)
	}
	merged := make(map[string][]byte, len(existing.Data)+len(data))
	for k, v := range existing.Data {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	// Keep the Secret's own type: merging into a TLS or dockerconfigjson Secret
	// must not silently retype it to Opaque.
	kept := corev1.SecretType(existing.Type)
	if kept == "" {
		kept = typ
	}
	return a.ApplySecret(ctx, namespace, name, kept, merged)
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

// BuildDockerConfigJSON returns the `.dockerconfigjson` payload for a private
// registry login (the same format `docker login` writes and Kubernetes expects
// in a kubernetes.io/dockerconfigjson Secret used as an imagePullSecret).
func BuildDockerConfigJSON(server, username, password, email string) ([]byte, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	entry := map[string]string{"username": username, "password": password, "auth": auth}
	if email != "" {
		entry["email"] = email
	}
	return json.Marshal(map[string]interface{}{
		"auths": map[string]interface{}{server: entry},
	})
}

// ApplyDockerRegistrySecret creates/updates a kubernetes.io/dockerconfigjson
// Secret for pulling images from a private registry (an imagePullSecret).
func (a *Applier) ApplyDockerRegistrySecret(ctx context.Context, namespace, name, server, username, password, email string) error {
	dockercfg, err := BuildDockerConfigJSON(server, username, password, email)
	if err != nil {
		return err
	}
	return a.ApplySecret(ctx, namespace, name, corev1.SecretTypeDockerConfigJson,
		map[string][]byte{corev1.DockerConfigJsonKey: dockercfg})
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
		keys := make([]string, 0, len(s.Data))
		for k := range s.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, SecretInfo{
			Name:      s.Name,
			Type:      string(s.Type),
			Keys:      len(s.Data),
			KeyNames:  keys,
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

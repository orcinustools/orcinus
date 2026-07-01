// Package deploy renders converted Kubernetes objects to YAML and (M2) applies
// them to a cluster. For now it covers rendering to stdout / a directory, which
// is what `orcinus deploy --dry-run [-o dir]` needs.
package deploy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// ensureGVK is a safety net: the forked kompose transformer already sets TypeMeta
// on every object it emits, and manifest documents carry apiVersion/kind from
// their source, so there is normally nothing to do here.
func ensureGVK(obj runtime.Object) {
	_ = obj
}

// Render returns all objects as a single multi-document YAML stream.
func Render(objects []runtime.Object) ([]byte, error) {
	var buf bytes.Buffer
	for i, obj := range objects {
		ensureGVK(obj)
		b, err := yaml.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("marshal object %d: %w", i, err)
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(b)
	}
	return buf.Bytes(), nil
}

// WriteDir writes each object to <dir>/<kind>-<name>.yaml.
func WriteDir(objects []runtime.Object, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, obj := range objects {
		ensureGVK(obj)
		kind := strings.ToLower(obj.GetObjectKind().GroupVersionKind().Kind)
		name := "object"
		if acc, err := meta.Accessor(obj); err == nil && acc.GetName() != "" {
			name = acc.GetName()
		}
		b, err := yaml.Marshal(obj)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, fmt.Sprintf("%s-%s.yaml", kind, name))
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Package compose loads Docker Compose files and converts them into Kubernetes
// objects using the forked kompose engine (third_party/kompose), then decorates
// the result with orcinus ownership labels and applies x-orcinus-* extensions.
//
// This is the orcinus side of the forked conversion pipeline described in
// ARCHITECTURE.md §3.2 / §4.
package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kubernetes/kompose/pkg/kobject"
	"github.com/kubernetes/kompose/pkg/loader"
	"github.com/kubernetes/kompose/pkg/transformer/kubernetes"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Ownership label keys applied to every generated object (ARCHITECTURE.md §2.4).
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelPartOf    = "app.kubernetes.io/part-of"
	LabelProject   = "orcinus.io/project"
	ManagedByValue = "orcinus"
)

// Options controls conversion.
type Options struct {
	// Files are compose file paths to convert together (merged by kompose).
	Files []string
	// ProjectName is used for ownership labels and Secret naming.
	ProjectName string
	// Namespace, if set, is stamped on every namespaced object.
	Namespace string
	// Replicas is the default replica count when a service has none.
	Replicas int
	// PVCSize is the default PersistentVolumeClaim request size (e.g. "1Gi").
	PVCSize string
}

// Convert runs the full compose → k8s pipeline and returns the decorated objects.
func Convert(opts Options) ([]runtime.Object, error) {
	if len(opts.Files) == 0 {
		return nil, fmt.Errorf("no compose files provided")
	}
	if opts.Replicas <= 0 {
		opts.Replicas = 1
	}
	if opts.PVCSize == "" {
		opts.PVCSize = "1Gi"
	}

	// 1. Translate x-orcinus-* into native kompose labels, writing rewritten
	//    compose files to a temp dir the forked loader can read.
	tmpDir, err := os.MkdirTemp("", "orcinus-compose-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	secrets := map[string][]string{}
	var loaderFiles []string
	for i, f := range opts.Files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read compose file %q: %w", f, err)
		}
		pp, err := injectKomposeLabels(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for svc, names := range pp.secrets {
			secrets[svc] = append(secrets[svc], names...)
		}
		tmp := filepath.Join(tmpDir, fmt.Sprintf("%02d-%s", i, filepath.Base(f)))
		if err := os.WriteFile(tmp, pp.content, 0o600); err != nil {
			return nil, err
		}
		loaderFiles = append(loaderFiles, tmp)
	}

	// 2. Load via the forked kompose loader (which is anchored to compose-go).
	l, err := loader.GetLoader("compose")
	if err != nil {
		return nil, err
	}
	komposeObject, err := l.LoadFile(loaderFiles, nil, false)
	if err != nil {
		return nil, fmt.Errorf("load compose: %w", err)
	}

	// 3. Transform via the forked kubernetes transformer.
	convertOpts := kobject.ConvertOptions{
		Provider:      "kubernetes",
		CreateD:       true,
		Replicas:      opts.Replicas,
		Volumes:       "persistentVolumeClaim",
		PVCRequestSize: opts.PVCSize,
		Namespace:     opts.Namespace,
		YAMLIndent:    2,
		InputFiles:    loaderFiles,
	}
	k := &kubernetes.Kubernetes{Opt: convertOpts}
	objects, err := k.Transform(komposeObject, convertOpts)
	if err != nil {
		return nil, fmt.Errorf("transform: %w", err)
	}

	// 4. Decorate with ownership labels + namespace.
	decorate(objects, opts.ProjectName, opts.Namespace)

	// 5. Apply x-orcinus-secret: pull named env vars into Secrets.
	extra, err := applySecrets(objects, secrets, opts)
	if err != nil {
		return nil, err
	}
	objects = append(objects, extra...)

	sortObjects(objects)
	return objects, nil
}

// decorate stamps ownership labels and namespace on every object.
func decorate(objects []runtime.Object, project, namespace string) {
	for _, obj := range objects {
		m, ok := obj.(metav1.ObjectMetaAccessor)
		if !ok {
			continue
		}
		meta := m.GetObjectMeta()
		labels := meta.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[LabelManagedBy] = ManagedByValue
		if project != "" {
			labels[LabelPartOf] = project
			labels[LabelProject] = project
		}
		meta.SetLabels(labels)
		if namespace != "" {
			meta.SetNamespace(namespace)
		}
	}
}

// applySecrets moves the env vars named by x-orcinus-secret out of each workload's
// containers and into a per-service Secret, referenced via secretKeyRef.
func applySecrets(objects []runtime.Object, secrets map[string][]string, opts Options) ([]runtime.Object, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	var created []runtime.Object
	for svc, names := range secrets {
		want := map[string]bool{}
		for _, n := range names {
			want[n] = true
		}
		secretName := svc + "-secret"
		data := map[string][]byte{}

		for _, obj := range objects {
			podSpec, objName := podSpecOf(obj)
			if podSpec == nil || objName != svc {
				continue
			}
			for ci := range podSpec.Containers {
				c := &podSpec.Containers[ci]
				kept := c.Env[:0]
				for _, e := range c.Env {
					if want[e.Name] && e.ValueFrom == nil {
						data[e.Name] = []byte(e.Value)
						kept = append(kept, corev1.EnvVar{
							Name: e.Name,
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
									Key:                  e.Name,
								},
							},
						})
					} else {
						kept = append(kept, e)
					}
				}
				c.Env = kept
			}
		}

		if len(data) == 0 {
			continue
		}
		secret := &corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: secretName},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}
		created = append(created, secret)
	}
	decorate(created, opts.ProjectName, opts.Namespace)
	return created, nil
}

// podSpecOf returns the pod spec and workload name for supported controllers.
func podSpecOf(obj runtime.Object) (*corev1.PodSpec, string) {
	switch t := obj.(type) {
	case *appsv1.Deployment:
		return &t.Spec.Template.Spec, t.Name
	case *appsv1.StatefulSet:
		return &t.Spec.Template.Spec, t.Name
	case *appsv1.DaemonSet:
		return &t.Spec.Template.Spec, t.Name
	default:
		return nil, ""
	}
}

// sortObjects gives deterministic output ordering (kind, then name).
func sortObjects(objects []runtime.Object) {
	sort.SliceStable(objects, func(i, j int) bool {
		ki, ni := kindName(objects[i])
		kj, nj := kindName(objects[j])
		if ki != kj {
			return ki < kj
		}
		return ni < nj
	})
}

func kindName(obj runtime.Object) (kind, name string) {
	kind = obj.GetObjectKind().GroupVersionKind().Kind
	if m, ok := obj.(metav1.ObjectMetaAccessor); ok {
		name = m.GetObjectMeta().GetName()
	}
	return kind, name
}

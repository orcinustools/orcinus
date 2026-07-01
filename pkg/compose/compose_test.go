package compose

import (
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const fixture = `
services:
  web:
    image: nginx:1.27
    ports: ["80:80"]
    deploy:
      replicas: 3
    x-orcinus-expose: ingress
    x-orcinus-host: web.example
  db:
    image: postgres:16
    environment:
      - POSTGRES_PASSWORD=secret
    volumes:
      - dbdata:/var/lib/postgresql/data
    x-orcinus-controller: statefulset
    x-orcinus-volume-size: 7Gi
    x-orcinus-secret:
      - POSTGRES_PASSWORD
volumes:
  dbdata: {}
`

func convertFixture(t *testing.T) []runtime.Object {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(f, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	objs, err := Convert(Options{Files: []string{f}, ProjectName: "proj", Namespace: "demo"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return objs
}

func TestConvertControllersAndLabels(t *testing.T) {
	objs := convertFixture(t)

	var web *appsv1.Deployment
	var db *appsv1.StatefulSet
	for _, o := range objs {
		switch v := o.(type) {
		case *appsv1.Deployment:
			if v.Name == "web" {
				web = v
			}
		case *appsv1.StatefulSet:
			if v.Name == "db" {
				db = v
			}
		}
	}
	if web == nil {
		t.Fatal("web Deployment not found")
	}
	if db == nil {
		t.Fatal("db StatefulSet not found (x-orcinus-controller: statefulset)")
	}
	if got := *web.Spec.Replicas; got != 3 {
		t.Errorf("web replicas = %d, want 3", got)
	}
	// Ownership labels + namespace.
	if web.Namespace != "demo" {
		t.Errorf("web namespace = %q, want demo", web.Namespace)
	}
	if web.Labels[LabelManagedBy] != ManagedByValue {
		t.Errorf("web missing managed-by label")
	}
	if web.Labels[LabelProject] != "proj" {
		t.Errorf("web project label = %q, want proj", web.Labels[LabelProject])
	}
}

func TestConvertVolumeSize(t *testing.T) {
	objs := convertFixture(t)
	found := false
	for _, o := range objs {
		if pvc, ok := o.(*corev1.PersistentVolumeClaim); ok && pvc.Name == "dbdata" {
			found = true
			q := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			if q.String() != "7Gi" {
				t.Errorf("PVC size = %s, want 7Gi", q.String())
			}
		}
	}
	if !found {
		t.Error("dbdata PVC not found")
	}
}

func TestConvertSecretExtraction(t *testing.T) {
	objs := convertFixture(t)

	var secret *corev1.Secret
	var db *appsv1.StatefulSet
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Secret:
			secret = v
		case *appsv1.StatefulSet:
			db = v
		}
	}
	if secret == nil {
		t.Fatal("expected a Secret from x-orcinus-secret")
	}
	if _, ok := secret.Data["POSTGRES_PASSWORD"]; !ok {
		t.Errorf("secret missing POSTGRES_PASSWORD key")
	}
	if string(secret.Data["POSTGRES_PASSWORD"]) != "secret" {
		t.Errorf("secret value = %q, want secret", secret.Data["POSTGRES_PASSWORD"])
	}
	// The env var in the workload must now reference the Secret, not hold the value.
	for _, c := range db.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "POSTGRES_PASSWORD" {
				if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
					t.Errorf("POSTGRES_PASSWORD not converted to secretKeyRef")
				}
				if e.Value != "" {
					t.Errorf("POSTGRES_PASSWORD still has inline value %q", e.Value)
				}
			}
		}
	}
}

func TestConvertIngress(t *testing.T) {
	objs := convertFixture(t)
	for _, o := range objs {
		if ing, ok := o.(*networkingv1.Ingress); ok {
			if len(ing.Spec.Rules) == 0 || ing.Spec.Rules[0].Host != "web.example" {
				t.Errorf("ingress host = %v, want web.example", ing.Spec.Rules)
			}
			return
		}
	}
	t.Error("expected an Ingress from x-orcinus-expose: ingress")
}

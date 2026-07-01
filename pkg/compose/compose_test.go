package compose

import (
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
)

const richFixture = `
services:
  api:
    image: myapi:1.0
    ports:
      - "8080:8080"
      - "9090:9090"
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: 256M
        reservations:
          cpus: "0.25"
          memory: 128M
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/health"]
      interval: 10s
      timeout: 3s
      retries: 3
    x-orcinus-expose: nodeport
`

func firstDeployment(t *testing.T, objs []runtime.Object) *appsv1.Deployment {
	t.Helper()
	for _, o := range objs {
		if d, ok := o.(*appsv1.Deployment); ok {
			return d
		}
	}
	t.Fatal("no Deployment found")
	return nil
}

func TestConvertResources(t *testing.T) {
	dep := firstDeployment(t, convertString(t, richFixture))
	res := dep.Spec.Template.Spec.Containers[0].Resources

	if got := res.Limits[corev1.ResourceCPU]; res.Limits.Cpu().Cmp(resource.MustParse("500m")) != 0 {
		t.Errorf("cpu limit = %s, want 500m", got.String())
	}
	if res.Limits.Memory().IsZero() {
		t.Errorf("memory limit is zero, want 256M")
	}
	if res.Requests.Cpu().Cmp(resource.MustParse("250m")) != 0 {
		t.Errorf("cpu request = %s, want 250m", res.Requests.Cpu().String())
	}
}

func TestConvertHealthcheckProbe(t *testing.T) {
	dep := firstDeployment(t, convertString(t, richFixture))
	probe := dep.Spec.Template.Spec.Containers[0].LivenessProbe
	if probe == nil {
		t.Fatal("expected a livenessProbe from compose healthcheck")
	}
	if probe.Exec == nil || len(probe.Exec.Command) == 0 {
		t.Errorf("expected an exec probe, got %+v", probe)
	}
}

func TestConvertMultiplePortsNodePort(t *testing.T) {
	objs := convertString(t, richFixture)
	for _, o := range objs {
		if svc, ok := o.(*corev1.Service); ok {
			if svc.Spec.Type != corev1.ServiceTypeNodePort {
				t.Errorf("service type = %s, want NodePort", svc.Spec.Type)
			}
			if len(svc.Spec.Ports) != 2 {
				t.Errorf("service ports = %d, want 2", len(svc.Spec.Ports))
			}
			return
		}
	}
	t.Error("no Service found")
}

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

func convertString(t *testing.T, content string) []runtime.Object {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	objs, err := Convert(Options{Files: []string{f}, ProjectName: "proj", Namespace: "demo"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return objs
}

func convertFixture(t *testing.T) []runtime.Object {
	t.Helper()
	return convertString(t, fixture)
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

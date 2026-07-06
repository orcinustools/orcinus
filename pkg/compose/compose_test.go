package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func TestConvertIngressTLSSugar(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: app.example.com
    x-orcinus-tls: letsencrypt
    x-orcinus-ingress-class: traefik
    x-orcinus-path: /
`
	for _, o := range convertString(t, f) {
		ing, ok := o.(*networkingv1.Ingress)
		if !ok {
			continue
		}
		if got := ing.Annotations["cert-manager.io/cluster-issuer"]; got != "letsencrypt" {
			t.Errorf("cluster-issuer annotation = %q, want letsencrypt", got)
		}
		if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "traefik" {
			t.Errorf("ingressClassName = %v, want traefik", ing.Spec.IngressClassName)
		}
		if len(ing.Spec.TLS) == 0 || ing.Spec.TLS[0].SecretName != "web-tls" {
			t.Fatalf("expected TLS block with secret web-tls, got %+v", ing.Spec.TLS)
		}
		if ing.Spec.TLS[0].Hosts[0] != "app.example.com" {
			t.Errorf("TLS host = %v, want app.example.com", ing.Spec.TLS[0].Hosts)
		}
		return
	}
	t.Fatal("no Ingress found")
}

func TestConvertAutoscale(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-autoscale-min: 2
    x-orcinus-autoscale-max: 6
    x-orcinus-autoscale-cpu: 70
`
	for _, o := range convertString(t, f) {
		hpa, ok := o.(*autoscalingv2.HorizontalPodAutoscaler)
		if !ok {
			continue
		}
		if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 6 {
			t.Errorf("min/max = %v/%d, want 2/6", hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
		}
		if hpa.Spec.ScaleTargetRef.Kind != "Deployment" || hpa.Spec.ScaleTargetRef.Name != "web" {
			t.Errorf("target = %s/%s, want Deployment/web", hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name)
		}
		if len(hpa.Spec.Metrics) == 0 || hpa.Spec.Metrics[0].Resource == nil ||
			*hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 70 {
			t.Errorf("expected CPU target 70, got %+v", hpa.Spec.Metrics)
		}
		return
	}
	t.Fatal("no HorizontalPodAutoscaler found")
}

func TestConvertStrategy(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-strategy: recreate
  api:
    image: nginx:1.27
    ports: ["8080"]
    x-orcinus-strategy: rolling
    x-orcinus-max-unavailable: "0"
    x-orcinus-max-surge: "50%"
`
	var web, api *appsv1.Deployment
	for _, o := range convertString(t, f) {
		if d, ok := o.(*appsv1.Deployment); ok {
			switch d.Name {
			case "web":
				web = d
			case "api":
				api = d
			}
		}
	}
	if web == nil || api == nil {
		t.Fatal("web/api Deployment not found")
	}
	if web.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("web strategy = %q, want Recreate", web.Spec.Strategy.Type)
	}
	if api.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType {
		t.Errorf("api strategy = %q, want RollingUpdate", api.Spec.Strategy.Type)
	}
	if api.Spec.Strategy.RollingUpdate == nil ||
		api.Spec.Strategy.RollingUpdate.MaxUnavailable.String() != "0" ||
		api.Spec.Strategy.RollingUpdate.MaxSurge.String() != "50%" {
		t.Errorf("api rolling knobs = %+v", api.Spec.Strategy.RollingUpdate)
	}
}

func TestConvertUpdateConfig(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    deploy:
      update_config:
        order: start-first
        parallelism: 2
        delay: 10s
        monitor: 60s
`
	dep := firstDeployment(t, convertString(t, f))
	s := dep.Spec.Strategy
	if s.Type != appsv1.RollingUpdateDeploymentStrategyType || s.RollingUpdate == nil {
		t.Fatalf("strategy = %+v, want RollingUpdate", s)
	}
	if s.RollingUpdate.MaxSurge.String() != "2" || s.RollingUpdate.MaxUnavailable.String() != "0" {
		t.Errorf("surge/unavail = %s/%s, want 2/0", s.RollingUpdate.MaxSurge, s.RollingUpdate.MaxUnavailable)
	}
	if dep.Spec.MinReadySeconds != 10 {
		t.Errorf("minReadySeconds = %d, want 10", dep.Spec.MinReadySeconds)
	}
	if dep.Spec.ProgressDeadlineSeconds == nil || *dep.Spec.ProgressDeadlineSeconds != 60 {
		t.Errorf("progressDeadlineSeconds = %v, want 60", dep.Spec.ProgressDeadlineSeconds)
	}
}

func TestConvertRollout(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-rollout: canary
  api:
    image: nginx:1.27
    ports: ["8080"]
    x-orcinus-rollout: bluegreen
`
	rollouts := map[string]*unstructured.Unstructured{}
	for _, o := range convertString(t, f) {
		if d, ok := o.(*appsv1.Deployment); ok && (d.Name == "web" || d.Name == "api") {
			t.Fatalf("%s should be a Rollout, not a Deployment", d.Name)
		}
		if u, ok := o.(*unstructured.Unstructured); ok && u.GetKind() == "Rollout" {
			rollouts[u.GetName()] = u
		}
	}
	if rollouts["web"] == nil || rollouts["api"] == nil {
		t.Fatalf("expected web+api Rollouts, got %v", rollouts)
	}
	if _, ok, _ := unstructured.NestedSlice(rollouts["web"].Object, "spec", "strategy", "canary", "steps"); !ok {
		t.Errorf("web should have canary steps")
	}
	svc, _, _ := unstructured.NestedString(rollouts["api"].Object, "spec", "strategy", "blueGreen", "activeService")
	if svc != "api" {
		t.Errorf("api blueGreen activeService = %q, want api", svc)
	}
	// The template's null creationTimestamp must be stripped (CRD schema).
	if _, ok, _ := unstructured.NestedFieldNoCopy(rollouts["web"].Object, "spec", "template", "metadata", "creationTimestamp"); ok {
		t.Errorf("web rollout template still has creationTimestamp")
	}
}

func TestConvertIngressCustomCert(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: app.example.com
    x-orcinus-tls-secret: my-cert
`
	for _, o := range convertString(t, f) {
		ing, ok := o.(*networkingv1.Ingress)
		if !ok {
			continue
		}
		if len(ing.Spec.TLS) == 0 || ing.Spec.TLS[0].SecretName != "my-cert" {
			t.Fatalf("expected TLS secret my-cert, got %+v", ing.Spec.TLS)
		}
		if _, has := ing.Annotations["cert-manager.io/cluster-issuer"]; has {
			t.Errorf("custom cert should NOT set a cert-manager annotation")
		}
		return
	}
	t.Fatal("no Ingress found")
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

// TestConvertTraefikMiddleware: x-orcinus-strip-prefix generates a StripPrefix
// Middleware CRD and the router.middlewares annotation lists strip-prefix first,
// then the named middlewares, in order and namespace-qualified.
func TestConvertTraefikMiddleware(t *testing.T) {
	const f = `
services:
  api:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: shop.example.com
    x-orcinus-path: /api
    x-orcinus-strip-prefix: true
    x-orcinus-middleware: [ratelimit, secure-headers]
`
	objs := convertString(t, f) // Namespace "demo"

	var ing *networkingv1.Ingress
	var strip *unstructured.Unstructured
	for _, o := range objs {
		switch t := o.(type) {
		case *networkingv1.Ingress:
			ing = t
		case *unstructured.Unstructured:
			if t.GetKind() == "Middleware" {
				strip = t
			}
		}
	}
	if ing == nil {
		t.Fatal("no Ingress generated")
	}
	got := ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]
	want := "demo-api-stripprefix@kubernetescrd,demo-ratelimit@kubernetescrd,demo-secure-headers@kubernetescrd"
	if got != want {
		t.Errorf("router.middlewares = %q, want %q", got, want)
	}
	if strip == nil {
		t.Fatal("no StripPrefix Middleware CRD generated")
	}
	if strip.GetAPIVersion() != "traefik.io/v1alpha1" {
		t.Errorf("middleware apiVersion = %q", strip.GetAPIVersion())
	}
	prefixes, _, _ := unstructured.NestedStringSlice(strip.Object, "spec", "stripPrefix", "prefixes")
	if len(prefixes) != 1 || prefixes[0] != "/api" {
		t.Errorf("stripPrefix prefixes = %v, want [/api]", prefixes)
	}
	if strip.GetLabels()[LabelManagedBy] != ManagedByValue {
		t.Errorf("middleware missing ownership label")
	}
}

// TestConvertPlacement: Swarm deploy.placement + x-orcinus-node-selector map to
// nodeAffinity / topologySpread / nodeSelector.
func TestConvertPlacement(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    x-orcinus-node-selector:
      disktype: ssd
    deploy:
      placement:
        constraints:
          - node.role == manager
          - node.platform.arch == amd64
          - node.labels.zone != west
        preferences:
          - spread: node.labels.zone
`
	for _, o := range convertString(t, f) {
		dep, ok := o.(*appsv1.Deployment)
		if !ok {
			continue
		}
		ps := dep.Spec.Template.Spec
		if ps.NodeSelector["disktype"] != "ssd" {
			t.Errorf("nodeSelector = %v, want disktype=ssd", ps.NodeSelector)
		}
		if ps.Affinity == nil || ps.Affinity.NodeAffinity == nil ||
			ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			t.Fatal("no required nodeAffinity")
		}
		exprs := ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions
		got := map[string]string{}
		for _, e := range exprs {
			got[e.Key] = string(e.Operator)
		}
		if got["node-role.kubernetes.io/control-plane"] != "Exists" {
			t.Errorf("node.role==manager → %v, want control-plane Exists", got)
		}
		if got["kubernetes.io/arch"] != "In" {
			t.Errorf("arch op = %q, want In", got["kubernetes.io/arch"])
		}
		if got["zone"] != "NotIn" {
			t.Errorf("zone op = %q, want NotIn", got["zone"])
		}
		if len(ps.TopologySpreadConstraints) != 1 || ps.TopologySpreadConstraints[0].TopologyKey != "zone" {
			t.Errorf("topologySpread = %+v, want key zone", ps.TopologySpreadConstraints)
		}
		return
	}
	t.Fatal("no Deployment generated")
}

// TestConvertPlacementUnsupported: an unknown constraint key is a hard error.
func TestConvertPlacementUnsupported(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(fp, []byte(`
services:
  web:
    image: nginx:1.27
    deploy:
      placement:
        constraints:
          - node.id == abc123
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Convert(Options{Files: []string{fp}, ProjectName: "p", Namespace: "d"}); err == nil {
		t.Fatal("expected error for unsupported constraint key node.id")
	}
}

// TestConvertBindMounts: host-path volumes → hostPath, named volumes → PVC.
func TestConvertBindMounts(t *testing.T) {
	const f = `
services:
  app:
    image: nginx:1.27
    volumes:
      - /srv/data:/data
      - ./conf:/etc/app:ro
      - cache:/var/cache
volumes:
  cache:
`
	var dep *appsv1.Deployment
	pvcs := 0
	for _, o := range convertString(t, f) {
		switch v := o.(type) {
		case *appsv1.Deployment:
			dep = v
		case *corev1.PersistentVolumeClaim:
			pvcs++
		}
	}
	if dep == nil {
		t.Fatal("no Deployment")
	}
	if pvcs != 1 {
		t.Errorf("PVC count = %d, want 1 (only the named volume)", pvcs)
	}
	hostPaths := map[string]string{} // path -> present
	for _, vol := range dep.Spec.Template.Spec.Volumes {
		if vol.HostPath != nil {
			hostPaths[vol.HostPath.Path] = vol.Name
		}
	}
	if _, ok := hostPaths["/srv/data"]; !ok {
		t.Errorf("missing hostPath /srv/data; got %v", hostPaths)
	}
	foundConf := false
	for p := range hostPaths {
		if strings.HasSuffix(p, "/conf") { // relative ./conf resolved to absolute
			foundConf = true
		}
	}
	if !foundConf {
		t.Errorf("missing resolved hostPath for ./conf; got %v", hostPaths)
	}
	// the read-only mount must carry ReadOnly.
	ro := false
	for _, m := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.MountPath == "/etc/app" && m.ReadOnly {
			ro = true
		}
	}
	if !ro {
		t.Errorf("/etc/app mount should be readOnly")
	}
}

// TestConvertMultiDomainTLS: x-orcinus-host with a comma list → multiple Ingress
// rules and a TLS block covering every host.
func TestConvertMultiDomainTLS(t *testing.T) {
	const f = `
services:
  web:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: "a.example.com,b.example.com"
    x-orcinus-tls: letsencrypt
`
	for _, o := range convertString(t, f) {
		ing, ok := o.(*networkingv1.Ingress)
		if !ok {
			continue
		}
		if len(ing.Spec.Rules) != 2 {
			t.Fatalf("ingress rules = %d, want 2", len(ing.Spec.Rules))
		}
		if len(ing.Spec.TLS) == 0 || len(ing.Spec.TLS[0].Hosts) != 2 {
			t.Fatalf("TLS hosts = %v, want both domains", ing.Spec.TLS)
		}
		return
	}
	t.Fatal("no Ingress generated")
}

// TestConvertImagePullSecret: x-orcinus-image-pull-secret → pod imagePullSecrets.
func TestConvertImagePullSecret(t *testing.T) {
	const f = `
services:
  app:
    image: registry.example.com/team/app:1.0
    ports: ["8080"]
    x-orcinus-image-pull-secret: [regcred, ghcr]
`
	for _, o := range convertString(t, f) {
		dep, ok := o.(*appsv1.Deployment)
		if !ok {
			continue
		}
		ps := dep.Spec.Template.Spec.ImagePullSecrets
		if len(ps) != 2 || ps[0].Name != "regcred" || ps[1].Name != "ghcr" {
			t.Fatalf("imagePullSecrets = %+v, want [regcred ghcr]", ps)
		}
		return
	}
	t.Fatal("no Deployment generated")
}

// TestConvertStripPrefixExplicit: an explicit prefix list is used verbatim.
func TestConvertStripPrefixExplicit(t *testing.T) {
	const f = `
services:
  api:
    image: nginx:1.27
    ports: ["80"]
    x-orcinus-expose: ingress
    x-orcinus-host: shop.example.com
    x-orcinus-strip-prefix: ["/v1", "/v2"]
`
	for _, o := range convertString(t, f) {
		u, ok := o.(*unstructured.Unstructured)
		if !ok || u.GetKind() != "Middleware" {
			continue
		}
		prefixes, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "stripPrefix", "prefixes")
		if len(prefixes) != 2 || prefixes[0] != "/v1" || prefixes[1] != "/v2" {
			t.Fatalf("prefixes = %v, want [/v1 /v2]", prefixes)
		}
		return
	}
	t.Fatal("no StripPrefix Middleware generated")
}

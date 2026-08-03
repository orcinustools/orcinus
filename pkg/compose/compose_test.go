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

// TestConvertVM: x-orcinus-vm turns a service into a KubeVirt VirtualMachine that
// the service's own Service still selects, with cpu/memory taken from compose.
func TestConvertVM(t *testing.T) {
	const f = `
services:
  ubuntu:
    image: quay.io/containerdisks/ubuntu:24.04
    ports: ["22"]
    x-orcinus-vm: true
    deploy:
      resources:
        limits:
          cpus: "2"
          memory: 2G
    x-orcinus-cloud-init-placeholder: unused
  web:
    image: nginx:1.27
    ports: ["80"]
`
	objs := convertString(t, f)
	var vm *unstructured.Unstructured
	var svc *corev1.Service
	for _, o := range objs {
		switch t2 := o.(type) {
		case *appsv1.Deployment:
			if t2.Name == "ubuntu" {
				t.Fatal("ubuntu should be a VirtualMachine, not a Deployment")
			}
		case *corev1.Service:
			if t2.Name == "ubuntu" {
				svc = t2
			}
		case *unstructured.Unstructured:
			if t2.GetKind() == "VirtualMachine" {
				vm = t2
			}
		}
	}
	if vm == nil || svc == nil {
		t.Fatalf("expected a VirtualMachine + its Service, got vm=%v svc=%v", vm, svc)
	}
	if vm.GetAPIVersion() != "kubevirt.io/v1" || vm.GetName() != "ubuntu" {
		t.Errorf("vm = %s/%s", vm.GetAPIVersion(), vm.GetName())
	}
	if rs, _, _ := unstructured.NestedString(vm.Object, "spec", "runStrategy"); rs != "Always" {
		t.Errorf("runStrategy = %q, want Always", rs)
	}
	// The Service must still select the workload: VM pod labels == Service selector.
	podLabels, _, _ := unstructured.NestedStringMap(vm.Object, "spec", "template", "metadata", "labels")
	for k, v := range svc.Spec.Selector {
		if podLabels[k] != v {
			t.Errorf("Service selector %s=%s not on the VM pod labels %v", k, v, podLabels)
		}
	}
	// compose image → containerDisk; cpus/memory → domain.
	img, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "spec", "volumes")
	_ = img
	vols, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "volumes")
	if len(vols) != 2 {
		t.Fatalf("expected rootdisk + cloudinit volumes, got %d", len(vols))
	}
	root, _ := vols[0].(map[string]interface{})
	cd, _ := root["containerDisk"].(map[string]interface{})
	if cd == nil || cd["image"] != "quay.io/containerdisks/ubuntu:24.04" {
		t.Errorf("rootdisk = %v, want the compose image as a containerDisk", root)
	}
	cores, _, _ := unstructured.NestedInt64(vm.Object, "spec", "template", "spec", "domain", "cpu", "cores")
	if cores != 2 {
		t.Errorf("cores = %d, want 2", cores)
	}
	mem, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "spec", "domain", "memory", "guest")
	if mem != "2Gi" {
		t.Errorf("guest memory = %q, want 2Gi", mem)
	}
	// A plain service in the same file stays a Deployment.
	if d := findDeployment(objs, "web"); d == nil {
		t.Error("web should still be a Deployment")
	}
}

// TestConvertVMPersistentAndSSH: --disk adds a CDI DataVolume, and the SSH secret
// becomes accessCredentials; cloud-init is passed through verbatim.
func TestConvertVMPersistentAndSSH(t *testing.T) {
	const f = `
services:
  vm:
    image: quay.io/containerdisks/fedora:44
    ports: ["22"]
    x-orcinus-vm: halted
    x-orcinus-vm-disk: 20Gi
    x-orcinus-vm-ssh-secret: vm-ssh
    x-orcinus-vm-ssh-users: [orcinus]
    x-orcinus-vm-cloud-init: |
      #cloud-config
      hostname: box
`
	var vm, dv *unstructured.Unstructured
	for _, o := range convertString(t, f) {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		switch u.GetKind() {
		case "VirtualMachine":
			vm = u
		case "DataVolume":
			dv = u
		}
	}
	if vm == nil || dv == nil {
		t.Fatalf("expected a VirtualMachine + DataVolume, got vm=%v dv=%v", vm, dv)
	}
	if rs, _, _ := unstructured.NestedString(vm.Object, "spec", "runStrategy"); rs != "Halted" {
		t.Errorf("runStrategy = %q, want Halted", rs)
	}
	if url, _, _ := unstructured.NestedString(dv.Object, "spec", "source", "registry", "url"); url != "docker://quay.io/containerdisks/fedora:44" {
		t.Errorf("DataVolume source = %q", url)
	}
	if size, _, _ := unstructured.NestedString(dv.Object, "spec", "storage", "resources", "requests", "storage"); size != "20Gi" {
		t.Errorf("DataVolume size = %q, want 20Gi", size)
	}
	vols, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "volumes")
	root, _ := vols[0].(map[string]interface{})
	if root["containerDisk"] != nil {
		t.Errorf("with a disk size the root volume must be the DataVolume, got %v", root)
	}
	creds, _, _ := unstructured.NestedSlice(vm.Object, "spec", "template", "spec", "accessCredentials")
	if len(creds) != 1 {
		t.Fatalf("expected 1 accessCredential, got %d", len(creds))
	}
	c, _ := creds[0].(map[string]interface{})
	key, _ := c["sshPublicKey"].(map[string]interface{})
	src, _ := key["source"].(map[string]interface{})
	secret, _ := src["secret"].(map[string]interface{})
	if secret["secretName"] != "vm-ssh" {
		t.Errorf("secretName = %v, want vm-ssh", secret["secretName"])
	}
	prop, _ := key["propagationMethod"].(map[string]interface{})
	if prop["qemuGuestAgent"] == nil {
		t.Errorf("named users need qemuGuestAgent propagation, got %v", prop)
	}
	userData, _, _ := unstructured.NestedString(vm.Object, "spec", "template", "spec", "volumes")
	_ = userData
	ci, _ := vols[1].(map[string]interface{})
	nc, _ := ci["cloudInitNoCloud"].(map[string]interface{})
	if s, _ := nc["userData"].(string); !strings.Contains(s, "hostname: box") {
		t.Errorf("cloud-init not passed through: %q", nc["userData"])
	}
}

// TestConvertVMErrors covers the combinations that can't work.
func TestConvertVMErrors(t *testing.T) {
	cases := map[string]string{
		"replicas": `
services:
  vm:
    image: quay.io/containerdisks/ubuntu:24.04
    x-orcinus-vm: true
    deploy:
      replicas: 3
`,
		"rollout": `
services:
  vm:
    image: quay.io/containerdisks/ubuntu:24.04
    ports: ["80"]
    x-orcinus-vm: true
    x-orcinus-rollout: canary
`,
		"autoscale": `
services:
  vm:
    image: quay.io/containerdisks/ubuntu:24.04
    x-orcinus-vm: true
    x-orcinus-autoscale-max: 5
`,
		"bad value": `
services:
  vm:
    image: quay.io/containerdisks/ubuntu:24.04
    x-orcinus-vm: sometimes
`,
		"ssh users without secret": `
services:
  vm:
    image: quay.io/containerdisks/ubuntu:24.04
    x-orcinus-vm: true
    x-orcinus-vm-ssh-users: [orcinus]
`,
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "orcinus.yml")
			if err := os.WriteFile(path, []byte(f), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Convert(Options{Files: []string{path}, ProjectName: "p"}); err == nil {
				t.Fatalf("expected an error for %q", name)
			}
		})
	}
}

func findDeployment(objs []runtime.Object, name string) *appsv1.Deployment {
	for _, o := range objs {
		if d, ok := o.(*appsv1.Deployment); ok && d.Name == name {
			return d
		}
	}
	return nil
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

// TestConvertConfigsRelativeFile: a config with a relative file: path resolves
// (regression: it used to fail after the doc was copied to a temp dir).
func TestConvertConfigsRelativeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.conf"), []byte("k=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(fp, []byte(`
services:
  app:
    image: nginx:1.27
    configs:
      - source: cfg
        target: /etc/app.conf
configs:
  cfg:
    file: ./c.conf
`), 0o600); err != nil {
		t.Fatal(err)
	}
	objs, err := Convert(Options{Files: []string{fp}, ProjectName: "p", Namespace: "d"})
	if err != nil {
		t.Fatalf("Convert with relative config file: %v", err)
	}
	for _, o := range objs {
		if _, ok := o.(*corev1.ConfigMap); ok {
			return
		}
	}
	t.Fatal("no ConfigMap generated from configs:")
}

// TestConvertEndpointModeDNSRR: deploy.endpoint_mode: dnsrr → headless Service.
func TestConvertEndpointModeDNSRR(t *testing.T) {
	const f = `
services:
  db:
    image: postgres:16
    ports: ["5432"]
    deploy:
      endpoint_mode: dnsrr
`
	for _, o := range convertString(t, f) {
		if svc, ok := o.(*corev1.Service); ok {
			if svc.Spec.ClusterIP != "None" {
				t.Fatalf("dnsrr Service ClusterIP = %q, want None (headless)", svc.Spec.ClusterIP)
			}
			return
		}
	}
	t.Fatal("no Service generated")
}

// TestConvertGenericResourcesGPU: generic_resources → nvidia.com/gpu limit.
func TestConvertGenericResourcesGPU(t *testing.T) {
	const f = `
services:
  trainer:
    image: nginx:1.27
    deploy:
      resources:
        reservations:
          generic_resources:
            - discrete_resource_spec: { kind: gpu, value: 2 }
`
	for _, o := range convertString(t, f) {
		dep, ok := o.(*appsv1.Deployment)
		if !ok {
			continue
		}
		q := dep.Spec.Template.Spec.Containers[0].Resources.Limits["nvidia.com/gpu"]
		if q.String() != "2" {
			t.Fatalf("nvidia.com/gpu limit = %q, want 2", q.String())
		}
		return
	}
	t.Fatal("no Deployment generated")
}

// TestConvertDevicesGPU: modern Compose GPU syntax (deploy.resources.reservations
// .devices with capabilities:[gpu]) → nvidia.com/gpu limit.
func TestConvertDevicesGPU(t *testing.T) {
	const f = `
services:
  trainer:
    image: nginx:1.27
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 2
              capabilities: [gpu]
`
	for _, o := range convertString(t, f) {
		dep, ok := o.(*appsv1.Deployment)
		if !ok {
			continue
		}
		q := dep.Spec.Template.Spec.Containers[0].Resources.Limits["nvidia.com/gpu"]
		if q.String() != "2" {
			t.Fatalf("nvidia.com/gpu = %q, want 2 (devices syntax)", q.String())
		}
		return
	}
	t.Fatal("no Deployment generated")
}

// TestConvertProfiles: services with a non-active profile are skipped.
func TestConvertProfiles(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "docker-compose.yml")
	src := []byte(`
services:
  web:
    image: nginx:1.27
  debugger:
    image: busybox:1.36
    profiles: ["debug"]
`)
	if err := os.WriteFile(fp, src, 0o600); err != nil {
		t.Fatal(err)
	}
	has := func(profiles []string, name string) bool {
		objs, err := Convert(Options{Files: []string{fp}, ProjectName: "p", Namespace: "d", Profiles: profiles})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			if d, ok := o.(*appsv1.Deployment); ok && d.Name == name {
				return true
			}
		}
		return false
	}
	if has(nil, "debugger") {
		t.Error("debugger should be excluded without its profile")
	}
	if !has([]string{"debug"}, "debugger") {
		t.Error("debugger should be included with --profile debug")
	}
	if !has(nil, "web") {
		t.Error("web (no profile) should always be included")
	}
}

// TestConvertDeployModeGlobal: Swarm `deploy.mode: global` → DaemonSet.
func TestConvertDeployModeGlobal(t *testing.T) {
	const f = `
services:
  agent:
    image: nginx:1.27
    ports: ["80"]
    deploy:
      mode: global
`
	for _, o := range convertString(t, f) {
		if _, ok := o.(*appsv1.DaemonSet); ok {
			return
		}
		if _, ok := o.(*appsv1.Deployment); ok {
			t.Fatal("deploy.mode: global produced a Deployment, want DaemonSet")
		}
	}
	t.Fatal("no DaemonSet generated for deploy.mode: global")
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

// configMapNamed finds a generated ConfigMap by name.
func configMapNamed(t *testing.T, objs []runtime.Object, name string) *corev1.ConfigMap {
	t.Helper()
	for _, o := range objs {
		if cm, ok := o.(*corev1.ConfigMap); ok && cm.Name == name {
			return cm
		}
	}
	t.Fatalf("no ConfigMap %q in %d objects", name, len(objs))
	return nil
}

// envFromNames lists the ConfigMaps a deployment's first container pulls in.
func envFromNames(d *appsv1.Deployment) []string {
	var out []string
	for _, ef := range d.Spec.Template.Spec.Containers[0].EnvFrom {
		if ef.ConfigMapRef != nil {
			out = append(out, ef.ConfigMapRef.Name)
		}
	}
	return out
}

// writeProject lays out a compose file plus companion files and converts it.
// files maps a path relative to the project dir to its content; a path may
// start with "../" to land beside the project dir.
func writeProject(t *testing.T, compose string, files map[string]string) ([]runtime.Object, error) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fp := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(fp, []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	return Convert(Options{Files: []string{fp}, ProjectName: "proj", Namespace: "demo"})
}

// TestConvertEnvFile: env_file → ConfigMap the container pulls in with envFrom.
func TestConvertEnvFile(t *testing.T) {
	objs, err := writeProject(t, `
services:
  web:
    image: nginx:1.27
    env_file: .env
`, map[string]string{".env": "FOO=bar\nDB_HOST=postgres\n"})
	if err != nil {
		t.Fatalf("Convert with env_file: %v", err)
	}
	cm := configMapNamed(t, objs, "env")
	if cm.Data["FOO"] != "bar" || cm.Data["DB_HOST"] != "postgres" {
		t.Fatalf("ConfigMap data = %v, want FOO=bar DB_HOST=postgres", cm.Data)
	}
	if got := envFromNames(firstDeployment(t, objs)); len(got) != 1 || got[0] != "env" {
		t.Fatalf("envFrom = %v, want [env]", got)
	}
}

// TestConvertEnvFileForms: every path shape compose accepts resolves against
// the compose file's own directory, not the temp dir the fork reads from.
func TestConvertEnvFileForms(t *testing.T) {
	objs, err := writeProject(t, `
services:
  web:
    image: nginx:1.27
    env_file:
      - a.env
      - ./config/b.env
      - ../shared.env
`, map[string]string{
		"a.env":         "A=1\n",
		"config/b.env":  "B=2\n",
		"../shared.env": "S=3\n",
	})
	if err != nil {
		t.Fatalf("Convert with env_file list: %v", err)
	}
	for name, key := range map[string]string{"a-env": "A", "config-b-env": "B", "shared-env": "S"} {
		if cm := configMapNamed(t, objs, name); cm.Data[key] == "" {
			t.Fatalf("ConfigMap %q missing key %q: %v", name, key, cm.Data)
		}
	}
	if got := envFromNames(firstDeployment(t, objs)); len(got) != 3 {
		t.Fatalf("envFrom = %v, want 3 ConfigMaps", got)
	}
}

// TestConvertEnvFileAbsolute: an absolute path is staged like any other. The
// fork joins env_file onto its working dir, so absolute paths would otherwise
// be concatenated onto the temp dir.
func TestConvertEnvFileAbsolute(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "abs.env")
	if err := os.WriteFile(envPath, []byte("K=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	objs, err := writeProject(t, `
services:
  web:
    image: nginx:1.27
    env_file: `+envPath+`
`, nil)
	if err != nil {
		t.Fatalf("Convert with absolute env_file: %v", err)
	}
	if cm := configMapNamed(t, objs, "abs-env"); cm.Data["K"] != "v" {
		t.Fatalf("ConfigMap data = %v, want K=v", cm.Data)
	}
}

// TestConvertEnvFileLongForm: {path, required} entries work, and an optional
// missing file is skipped rather than failing the conversion.
func TestConvertEnvFileLongForm(t *testing.T) {
	objs, err := writeProject(t, `
services:
  web:
    image: nginx:1.27
    env_file:
      - path: ./a.env
        required: true
      - path: ./gone.env
        required: false
`, map[string]string{"a.env": "A=1\n"})
	if err != nil {
		t.Fatalf("Convert with long-form env_file: %v", err)
	}
	if cm := configMapNamed(t, objs, "a-env"); cm.Data["A"] != "1" {
		t.Fatalf("ConfigMap data = %v, want A=1", cm.Data)
	}
	if got := envFromNames(firstDeployment(t, objs)); len(got) != 1 {
		t.Fatalf("envFrom = %v, want only the required file", got)
	}
}

// TestConvertEnvFileMissing: a required env_file that is absent is reported
// against the path the user wrote, not the temp copy.
func TestConvertEnvFileMissing(t *testing.T) {
	_, err := writeProject(t, `
services:
  web:
    image: nginx:1.27
    env_file: ./nope.env
`, nil)
	if err == nil {
		t.Fatal("Convert succeeded with a missing required env_file")
	}
	if !strings.Contains(err.Error(), "nope.env") || !strings.Contains(err.Error(), `service "web"`) {
		t.Fatalf("error = %v, want it to name the service and ./nope.env", err)
	}
}

// TestConvertEnvFileSecret: x-orcinus-secret moves a key out of the env_file
// ConfigMap into a Secret, instead of leaving the value in plain config.
func TestConvertEnvFileSecret(t *testing.T) {
	objs, err := writeProject(t, `
services:
  web:
    image: nginx:1.27
    env_file: .env
    x-orcinus-secret:
      - DB_PASS
`, map[string]string{".env": "DB_PASS=s3cret\nAPP_ENV=prod\n"})
	if err != nil {
		t.Fatalf("Convert with env_file + x-orcinus-secret: %v", err)
	}
	cm := configMapNamed(t, objs, "env")
	if _, leaked := cm.Data["DB_PASS"]; leaked {
		t.Fatalf("DB_PASS left in ConfigMap: %v", cm.Data)
	}
	if cm.Data["APP_ENV"] != "prod" {
		t.Fatalf("APP_ENV = %q, want it left in the ConfigMap", cm.Data["APP_ENV"])
	}

	var secret *corev1.Secret
	for _, o := range objs {
		if s, ok := o.(*corev1.Secret); ok && s.Name == "web-secret" {
			secret = s
		}
	}
	if secret == nil {
		t.Fatal("no Secret web-secret generated")
	}
	if string(secret.Data["DB_PASS"]) != "s3cret" {
		t.Fatalf("Secret DB_PASS = %q, want s3cret", secret.Data["DB_PASS"])
	}

	c := firstDeployment(t, objs).Spec.Template.Spec.Containers[0]
	for _, e := range c.Env {
		if e.Name != "DB_PASS" {
			continue
		}
		if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("DB_PASS env = %+v, want a secretKeyRef", e)
		}
		return
	}
	t.Fatal("container has no DB_PASS env var sourced from the Secret")
}

// TestConvertDotEnvInterpolation: ${VAR} resolves from the project's .env,
// which compose-go reads from the loader's working directory.
func TestConvertDotEnvInterpolation(t *testing.T) {
	objs, err := writeProject(t, `
services:
  web:
    image: nginx:${TAG}
`, map[string]string{".env": "TAG=1.27\n"})
	if err != nil {
		t.Fatalf("Convert with .env interpolation: %v", err)
	}
	if got := firstDeployment(t, objs).Spec.Template.Spec.Containers[0].Image; got != "nginx:1.27" {
		t.Fatalf("image = %q, want nginx:1.27", got)
	}
}

// envFromSecretNames lists the Secrets a container loads with envFrom.
func envFromSecretNames(c corev1.Container) []string {
	var out []string
	for _, ef := range c.EnvFrom {
		if ef.SecretRef != nil {
			out = append(out, ef.SecretRef.Name)
		}
	}
	return out
}

// TestConvertEnvFromSecret: an existing Secret is loaded into the container env
// without generating a Secret of our own.
func TestConvertEnvFromSecret(t *testing.T) {
	objs := convertString(t, `
services:
  app:
    image: myapp:1.0
    x-orcinus-env-from-secret: app-secret
`)
	c := firstDeployment(t, objs).Spec.Template.Spec.Containers[0]
	if got := envFromSecretNames(c); len(got) != 1 || got[0] != "app-secret" {
		t.Fatalf("envFrom secretRefs = %v, want [app-secret]", got)
	}
	for _, o := range objs {
		if s, ok := o.(*corev1.Secret); ok {
			t.Fatalf("generated Secret %q, want the existing one referenced instead", s.Name)
		}
	}
}

// TestConvertEnvFromSecretList: several Secrets, in the order written.
func TestConvertEnvFromSecretList(t *testing.T) {
	objs := convertString(t, `
services:
  app:
    image: myapp:1.0
    x-orcinus-env-from-secret: [app-secret, extra-secret]
`)
	c := firstDeployment(t, objs).Spec.Template.Spec.Containers[0]
	got := envFromSecretNames(c)
	if len(got) != 2 || got[0] != "app-secret" || got[1] != "extra-secret" {
		t.Fatalf("envFrom secretRefs = %v, want [app-secret extra-secret]", got)
	}
}

// TestConvertEnvFromSecretAfterEnvFile: the Secret is appended after the
// env_file ConfigMap, so a key in both resolves to the Secret's value.
func TestConvertEnvFromSecretAfterEnvFile(t *testing.T) {
	objs, err := writeProject(t, `
services:
  app:
    image: myapp:1.0
    env_file: .env
    x-orcinus-env-from-secret: app-secret
`, map[string]string{".env": "APP_ENV=prod\n"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	c := firstDeployment(t, objs).Spec.Template.Spec.Containers[0]
	if len(c.EnvFrom) != 2 {
		t.Fatalf("envFrom = %+v, want the ConfigMap and the Secret", c.EnvFrom)
	}
	if c.EnvFrom[0].ConfigMapRef == nil || c.EnvFrom[0].ConfigMapRef.Name != "env" {
		t.Fatalf("envFrom[0] = %+v, want the env_file ConfigMap first", c.EnvFrom[0])
	}
	if c.EnvFrom[1].SecretRef == nil || c.EnvFrom[1].SecretRef.Name != "app-secret" {
		t.Fatalf("envFrom[1] = %+v, want the Secret last so it wins", c.EnvFrom[1])
	}
}

// TestConvertEnvFromSecretOtherControllers: applied before Rollout conversion,
// so every workload kind carries it.
func TestConvertEnvFromSecretOtherControllers(t *testing.T) {
	objs := convertString(t, `
services:
  sts:
    image: myapp:1.0
    x-orcinus-controller: statefulset
    x-orcinus-env-from-secret: app-secret
  canary:
    image: myapp:1.0
    x-orcinus-rollout: canary
    x-orcinus-env-from-secret: app-secret
`)
	var sawSTS, sawRollout bool
	for _, o := range objs {
		switch v := o.(type) {
		case *appsv1.StatefulSet:
			if got := envFromSecretNames(v.Spec.Template.Spec.Containers[0]); len(got) == 1 && got[0] == "app-secret" {
				sawSTS = true
			}
		case *unstructured.Unstructured:
			if v.GetKind() != "Rollout" {
				continue
			}
			cs, _, _ := unstructured.NestedSlice(v.Object, "spec", "template", "spec", "containers")
			if len(cs) == 0 {
				continue
			}
			c, _ := cs[0].(map[string]interface{})
			envFrom, _, _ := unstructured.NestedSlice(c, "envFrom")
			for _, ef := range envFrom {
				m, _ := ef.(map[string]interface{})
				if name, _, _ := unstructured.NestedString(m, "secretRef", "name"); name == "app-secret" {
					sawRollout = true
				}
			}
		}
	}
	if !sawSTS {
		t.Error("StatefulSet did not get the envFrom secretRef")
	}
	if !sawRollout {
		t.Error("Rollout did not inherit the envFrom secretRef")
	}
}

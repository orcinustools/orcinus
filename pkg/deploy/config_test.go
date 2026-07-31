package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// --- fixtures ------------------------------------------------------------

func webDeployment() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{
			"name": "web", "namespace": testNS, "labels": ownedLabels(),
			"resourceVersion": "4711", "uid": "abc-123", "generation": int64(2),
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "3"},
		},
		"spec": map[string]any{
			"replicas": int64(2),
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name":  "web",
						"image": "nginx:1.27",
						"args":  []any{"nginx", "-g", "daemon off;"},
						"env": []any{
							map[string]any{"name": "TZ", "value": "UTC"},
							map[string]any{"name": "PASSWORD", "valueFrom": map[string]any{
								"secretKeyRef": map[string]any{"name": "db-secret", "key": "pw"}}},
						},
						"resources":    map[string]any{"limits": map[string]any{"cpu": "500m", "memory": "256Mi"}},
						"volumeMounts": []any{map[string]any{"name": "conf", "mountPath": "/etc/nginx/conf.d"}},
					}},
					"volumes": []any{map[string]any{
						"name": "conf", "hostPath": map[string]any{"path": "/srv/conf"}}},
				},
			},
		},
		"status": map[string]any{"readyReplicas": int64(2)},
	}}
}

func redisStatefulSet() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "StatefulSet",
		"metadata": map[string]any{"name": "redis", "namespace": testNS, "labels": ownedLabels()},
		"spec": map[string]any{
			"replicas": int64(1),
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name":         "redis",
						"image":        "redis:7",
						"args":         []any{"redis-server", "--appendonly", "yes"},
						"volumeMounts": []any{map[string]any{"name": "redisdata", "mountPath": "/data"}},
					}},
				},
			},
			// A live cluster stamps each embedded PVC template with its own
			// creationTimestamp and status; both must be stripped on export.
			"volumeClaimTemplates": []any{map[string]any{
				"metadata": map[string]any{"name": "redisdata", "creationTimestamp": nil},
				"spec": map[string]any{"resources": map[string]any{
					"requests": map[string]any{"storage": "2Gi"}}},
				"status": map[string]any{"phase": "Pending"},
			}},
		},
	}}
}

func svcObj(name string, port, target int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": name, "namespace": testNS, "labels": ownedLabels()},
		"spec": map[string]any{
			"clusterIP": "10.43.0.7",
			"ports":     []any{map[string]any{"port": port, "targetPort": target}},
		},
	}}
}

func ingressObj(name, host, backend string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
		"metadata": map[string]any{"name": name, "namespace": testNS, "labels": ownedLabels()},
		"spec": map[string]any{"rules": []any{map[string]any{
			"host": host,
			"http": map[string]any{"paths": []any{map[string]any{
				"backend": map[string]any{"service": map[string]any{"name": backend}}}}},
		}}},
	}}
}

func secretObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "namespace": testNS, "labels": ownedLabels()},
		"data":     map[string]any{"pw": "c3VwZXItc2VjcmV0"},
	}}
}

func fullProject() *Applier {
	return newFakeApplier(
		webDeployment(), redisStatefulSet(),
		svcObj("web", 80, 8080), svcObj("redis", 6379, 6379),
		ingressObj("web", "web.local", "web"), secretObj("db-secret"),
	)
}

// --- tests ---------------------------------------------------------------

func TestProjectConfigCollectsOwnedResources(t *testing.T) {
	cfg, err := fullProject().ProjectConfig(context.Background(), testProject, testNS)
	if err != nil {
		t.Fatalf("ProjectConfig: %v", err)
	}
	if len(cfg.Items) != 6 {
		t.Fatalf("collected %d resources, want 6", len(cfg.Items))
	}
	if cfg.Project != testProject || cfg.Namespace != testNS {
		t.Errorf("got project %q ns %q", cfg.Project, cfg.Namespace)
	}
}

func TestProjectConfigErrors(t *testing.T) {
	a := fullProject()
	if _, err := a.ProjectConfig(context.Background(), "", testNS); err == nil {
		t.Error("an empty project name should be rejected")
	}
	if _, err := a.ProjectConfig(context.Background(), "nosuchproject", testNS); err == nil {
		t.Error("an unknown project should report that nothing was found")
	}
}

// k8s output must drop cluster bookkeeping so it can be applied elsewhere.
func TestRenderK8sIsPortable(t *testing.T) {
	cfg, err := fullProject().ProjectConfig(context.Background(), testProject, testNS)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.RenderK8s(false)
	if err != nil {
		t.Fatalf("RenderK8s: %v", err)
	}
	got := string(out)
	for _, banned := range []string{
		"resourceVersion", "uid: abc-123", "generation", "status:",
		"clusterIP", "deployment.kubernetes.io/revision", "creationTimestamp",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("export still carries %q", banned)
		}
	}
	if !strings.Contains(got, "kind: Deployment") || !strings.Contains(got, "kind: StatefulSet") {
		t.Error("export lost a workload")
	}
	if n := strings.Count(got, "\n---\n"); n != 5 {
		t.Errorf("got %d document separators, want 5 for 6 objects", n)
	}
	// Every document must still parse.
	for _, doc := range strings.Split(got, "\n---\n") {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatalf("export is not valid YAML: %v", err)
		}
	}
}

// Secrets are redacted unless explicitly requested.
func TestRenderK8sRedactsSecrets(t *testing.T) {
	cfg, err := fullProject().ProjectConfig(context.Background(), testProject, testNS)
	if err != nil {
		t.Fatal(err)
	}
	redacted, err := cfg.RenderK8s(false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(redacted), "c3VwZXItc2VjcmV0") {
		t.Error("secret value leaked into the default export")
	}
	if !strings.Contains(string(redacted), "<redacted>") {
		t.Error("expected a redaction marker")
	}

	shown, err := cfg.RenderK8s(true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shown), "c3VwZXItc2VjcmV0") {
		t.Error("--show-secrets should include the value")
	}
	// Redacting must not mutate the collected items.
	again, _ := cfg.RenderK8s(true)
	if !strings.Contains(string(again), "c3VwZXItc2VjcmV0") {
		t.Error("a redacted render corrupted the cached items")
	}
}

func TestRenderComposeRoundTrip(t *testing.T) {
	cfg, err := fullProject().ProjectConfig(context.Background(), testProject, testNS)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.RenderCompose()
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}

	var file struct {
		Services map[string]struct {
			Image      string            `json:"image"`
			Command    []string          `json:"command"`
			Ports      []string          `json:"ports"`
			Volumes    []string          `json:"volumes"`
			Env        map[string]string `json:"environment"`
			Controller string            `json:"x-orcinus-controller"`
			VolumeSize string            `json:"x-orcinus-volume-size"`
			Expose     string            `json:"x-orcinus-expose"`
			Host       string            `json:"x-orcinus-host"`
			Deploy     struct {
				Replicas  *int64 `json:"replicas"`
				Resources struct {
					Limits map[string]string `json:"limits"`
				} `json:"resources"`
			} `json:"deploy"`
		} `json:"services"`
		Volumes map[string]any `json:"volumes"`
	}
	if err := yaml.Unmarshal(out, &file); err != nil {
		t.Fatalf("reconstructed compose does not parse: %v\n%s", err, out)
	}

	web, ok := file.Services["web"]
	if !ok {
		t.Fatal("web service missing")
	}
	if web.Image != "nginx:1.27" {
		t.Errorf("web image = %q", web.Image)
	}
	if web.Controller != "" {
		t.Errorf("a Deployment must not emit x-orcinus-controller, got %q", web.Controller)
	}
	if got := strings.Join(web.Command, " "); got != "nginx -g daemon off;" {
		t.Errorf("k8s args should map to compose command, got %q", got)
	}
	if web.Deploy.Replicas == nil || *web.Deploy.Replicas != 2 {
		t.Errorf("web replicas = %v", web.Deploy.Replicas)
	}
	// compose spells it `cpus` and wants cores, not Kubernetes' milli-CPU.
	if web.Deploy.Resources.Limits["cpus"] != "0.5" {
		t.Errorf("cpu limit = %v, want cpus 0.5", web.Deploy.Resources.Limits)
	}
	if web.Deploy.Resources.Limits["memory"] != "256Mi" {
		t.Errorf("memory limit lost: %v", web.Deploy.Resources.Limits)
	}
	if len(web.Ports) != 1 || web.Ports[0] != "80:8080" {
		t.Errorf("web ports = %v, want [80:8080]", web.Ports)
	}
	if len(web.Volumes) != 1 || web.Volumes[0] != "/srv/conf:/etc/nginx/conf.d" {
		t.Errorf("hostPath should become a bind mount, got %v", web.Volumes)
	}
	if web.Env["TZ"] != "UTC" {
		t.Errorf("plain env lost: %v", web.Env)
	}
	if _, leaked := web.Env["PASSWORD"]; leaked {
		t.Error("a secretKeyRef env must not be inlined as a plain value")
	}
	if web.Expose != "ingress" || web.Host != "web.local" {
		t.Errorf("ingress not mapped back: expose=%q host=%q", web.Expose, web.Host)
	}

	redis, ok := file.Services["redis"]
	if !ok {
		t.Fatal("redis service missing")
	}
	if redis.Controller != "statefulset" {
		t.Errorf("redis controller = %q, want statefulset", redis.Controller)
	}
	if len(redis.Volumes) != 1 || redis.Volumes[0] != "redisdata:/data" {
		t.Errorf("volumeClaimTemplate should become a named volume, got %v", redis.Volumes)
	}
	if redis.VolumeSize != "2Gi" {
		t.Errorf("volume size = %q, want 2Gi", redis.VolumeSize)
	}
	if len(redis.Ports) != 1 || redis.Ports[0] != "6379" {
		t.Errorf("equal port/targetPort should collapse to one number, got %v", redis.Ports)
	}
	if _, ok := file.Volumes["redisdata"]; !ok {
		t.Errorf("named volume missing from the top-level volumes: %v", file.Volumes)
	}

	// The caveats belong in the output, not only in the docs.
	head := string(out)
	if !strings.HasPrefix(head, "#") || !strings.Contains(head, "note: web: env PASSWORD") {
		t.Errorf("expected a header noting the dropped env reference:\n%s", head)
	}
}

// A project with only supporting objects cannot be described as compose.
func TestRenderComposeNeedsAWorkload(t *testing.T) {
	a := newFakeApplier(svcObj("web", 80, 80))
	cfg, err := a.ProjectConfig(context.Background(), testProject, testNS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.RenderCompose(); err == nil {
		t.Error("expected an error when there is no workload to describe")
	}
}

func TestWriteSummary(t *testing.T) {
	cfg, err := fullProject().ProjectConfig(context.Background(), testProject, testNS)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := cfg.WriteSummary(&buf); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		testProject, "web", "nginx:1.27", "Deployment",
		"redis", "StatefulSet", "2/2", "Ingress:", "Secret:", "db-secret",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}
}

// Kubernetes resource limits must be translated into compose's spelling, or the
// exported file is rejected by the compose parser on the way back in.
func TestComposeLimits(t *testing.T) {
	got := composeLimits(map[string]string{
		"cpu": "500m", "memory": "256Mi", "ephemeral-storage": "1Gi",
	})
	if got["cpus"] != "0.5" {
		t.Errorf("cpus = %q, want 0.5", got["cpus"])
	}
	if got["memory"] != "256Mi" {
		t.Errorf("memory = %q", got["memory"])
	}
	if _, ok := got["cpu"]; ok {
		t.Error("the Kubernetes spelling `cpu` is not valid compose")
	}
	if _, ok := got["ephemeral-storage"]; ok {
		t.Error("limits compose has no equivalent for must be dropped")
	}

	for in, want := range map[string]string{
		"500m": "0.5", "1500m": "1.5", "2": "2", "0.25": "0.25", "bogus": "bogus",
	} {
		if got := milliCPUToCores(in); got != want {
			t.Errorf("milliCPUToCores(%q) = %q, want %q", in, got, want)
		}
	}
}

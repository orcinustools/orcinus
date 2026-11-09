package sign

import (
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPolicy(t *testing.T) {
	_, pub, _ := GenerateKey(nil)
	cfg := &Config{
		Mode:             "enforce",
		Keys:             map[string]string{"ci": string(pub), "alice": string(pub)},
		ExemptNamespaces: []string{"sandbox"},
		ExemptImages:     []string{"docker.io/library/busybox"},
		PluginImages:     []string{"quay.io/prometheus-operator/prometheus-operator:v0.77.1"},
		InsecureRegistry: true,
	}
	p := Policy(cfg)
	str := func(path ...string) string {
		v, _, _ := unstructured.NestedFieldNoCopy(p.Object, path...)
		return strings.TrimSpace(strings.ReplaceAll(fmtAny(v), "\n", " "))
	}

	if got := str("spec", "validationActions"); got != "[Deny]" {
		t.Errorf("validationActions = %s", got)
	}
	cel := str("spec", "validations")
	if !strings.Contains(cel, "verifyImageSignatures(image, [attestors.k0, attestors.k1]) > 0") ||
		!strings.Contains(cel, "images.initContainers") {
		t.Errorf("validation expression: %s", cel)
	}
	if ref := str("spec", "matchImageReferences"); !strings.Contains(ref, `ref.startsWith(p)`) || !strings.Contains(ref, "prometheus-operator") {
		t.Errorf("matchImageReferences: %s", ref)
	}
	ns := str("spec", "matchConstraints", "namespaceSelector")
	for _, want := range []string{"kube-system", "sandbox", "kyverno", "orcinus.io/plugin", "DoesNotExist"} {
		if !strings.Contains(ns, want) {
			t.Errorf("namespaceSelector lacks %s: %s", want, ns)
		}
	}
	if str("spec", "validationConfigurations", "mutateDigest") != "true" || str("spec", "credentials", "allowInsecureRegistry") != "true" {
		t.Error("mutateDigest / allowInsecureRegistry not set")
	}
	cfg.Mode = "audit"
	if got := Policy(cfg).Object["spec"].(map[string]any)["validationActions"]; fmtAny(got) != "[Audit]" {
		t.Errorf("audit mode: %v", got)
	}

	for _, c := range []struct {
		ns, image string
		want      bool
	}{
		{"kube-system", "x/y:1", true},
		{"sandbox", "x/y:1", true},
		{"default", "docker.io/library/busybox:1.36", true},
		{"default", "quay.io/prometheus-operator/prometheus-operator:v0.77.1", true},
		{"default", "quay.io/prometheus-operator/prometheus-operator:v0.77.1-evil", false},
		{"default", "registry.example.com/app:1", false},
	} {
		if got := cfg.Exempt(c.ns, c.image); got != c.want {
			t.Errorf("Exempt(%s, %s) = %v, want %v", c.ns, c.image, got, c.want)
		}
	}
}

func fmtAny(v any) string { return fmt.Sprint(v) }

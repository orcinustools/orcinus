package sign

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/plugin"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	// Namespace holds the settings ConfigMap; it is Kyverno's own namespace.
	Namespace  = "kyverno"
	configName = "orcinus-signing"
	policyName = "orcinus-image-signing"
)

// SystemNamespaces always run without signatures: the cluster's own pods and
// the policy engine. Plugin namespaces are exempt through their
// orcinus.io/plugin label.
var SystemNamespaces = []string{"kube-system", "kube-public", "kube-node-lease", Namespace}

var policyGVR = schema.GroupVersionResource{Group: "policies.kyverno.io", Version: "v1", Resource: "imagevalidatingpolicies"}

// Config is the cluster's signing settings, kept in the kyverno/orcinus-signing
// ConfigMap so any machine with the kubeconfig sees the same thing.
type Config struct {
	Mode             string            `json:"mode"` // off | audit | enforce
	Keys             map[string]string `json:"keys"` // name → public key PEM
	ExemptNamespaces []string          `json:"exemptNamespaces,omitempty"`
	ExemptImages     []string          `json:"exemptImages,omitempty"`     // prefixes
	PullSecrets      []string          `json:"pullSecrets,omitempty"`      // in Namespace
	InsecureRegistry bool              `json:"insecureRegistry,omitempty"` // allow plain-HTTP registries
	// PluginImages are the exact images plugins run in "default" (set by Apply).
	PluginImages []string `json:"pluginImages,omitempty"`
}

// PublicKeys parses the trusted keys.
func (c *Config) PublicKeys() (map[string]*ecdsa.PublicKey, error) {
	out := make(map[string]*ecdsa.PublicKey, len(c.Keys))
	for n, p := range c.Keys {
		k, err := ParsePublicKey([]byte(p))
		if err != nil {
			return nil, fmt.Errorf("trusted key %s: %w", n, err)
		}
		out[n] = k
	}
	return out, nil
}

// Exempt reports whether an image in a namespace runs unchecked. Plugin
// namespaces are not known here; callers check the namespace label.
func (c *Config) Exempt(namespace, image string) bool {
	for _, n := range append(SystemNamespaces, c.ExemptNamespaces...) {
		if n == namespace {
			return true
		}
	}
	for _, p := range c.ExemptImages {
		if strings.HasPrefix(image, p) {
			return true
		}
	}
	for _, p := range c.PluginImages {
		if image == p {
			return true
		}
	}
	return false
}

type clients struct {
	cs  kubernetes.Interface
	dyn dynamic.Interface
}

func connect(kubeconfig string) (*clients, error) {
	cfg, err := deploy.LoadRESTConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &clients{cs, dyn}, nil
}

// LoadConfig returns the cluster's signing settings, or nil when signing was
// never set up.
func LoadConfig(ctx context.Context, kubeconfig string) (*Config, error) {
	c, err := connect(kubeconfig)
	if err != nil {
		return nil, err
	}
	return c.load(ctx)
}

func (c *clients) load(ctx context.Context) (*Config, error) {
	cm, err := c.cs.CoreV1().ConfigMaps(Namespace).Get(ctx, configName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read signing settings: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal([]byte(cm.Data["config.json"]), &cfg); err != nil {
		return nil, fmt.Errorf("read signing settings: %w", err)
	}
	return &cfg, nil
}

// Apply stores the settings and makes the admission policy match them,
// installing Kyverno first if needed.
func Apply(ctx context.Context, kubeconfig string, cfg *Config) error {
	if cfg.Mode != "off" && len(cfg.Keys) == 0 {
		return errors.New("signing is on but no key is trusted; trust a key first, or turn signing off (--mode off)")
	}
	c, err := connect(kubeconfig)
	if err != nil {
		return err
	}
	if cfg.Mode != "off" && !plugin.Installed("kyverno") {
		if _, err := c.cs.AppsV1().Deployments(Namespace).Get(ctx, "kyverno-admission-controller", metav1.GetOptions{}); err != nil {
			fmt.Println("installing kyverno (the admission policy engine)...")
			if err := plugin.Install(ctx, "kyverno", plugin.Options{Kubeconfig: kubeconfig}); err != nil {
				return err
			}
		}
	}
	if cfg.Mode != "off" {
		// Plugins installed before signing was set up get their namespaces labeled now.
		if cfg.PluginImages, err = plugin.LabelInstalled(ctx, kubeconfig); err != nil {
			return err
		}
	}
	if _, err := c.cs.CoreV1().Namespaces().Get(ctx, Namespace, metav1.GetOptions{}); apierrors.IsNotFound(err) {
		if _, err := c.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}}, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	body, _ := json.MarshalIndent(cfg, "", "  ")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: Namespace},
		Data:       map[string]string{"config.json": string(body)},
	}
	if _, err := c.cs.CoreV1().ConfigMaps(Namespace).Update(ctx, cm, metav1.UpdateOptions{}); apierrors.IsNotFound(err) {
		_, err = c.cs.CoreV1().ConfigMaps(Namespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	policies := c.dyn.Resource(policyGVR)
	if cfg.Mode == "off" {
		err := policies.Delete(ctx, policyName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) && !isNoMatch(err) {
			return err
		}
		return nil
	}
	pol := Policy(cfg)
	data, _ := json.Marshal(pol.Object)
	_, err = policies.Patch(ctx, policyName, "application/apply-patch+yaml", data, metav1.PatchOptions{FieldManager: "orcinus", Force: ptr(true)})
	return err
}

func ptr[T any](v T) *T { return &v }

func isNoMatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), "the server could not find the requested resource")
}

// Policy renders the Kyverno ImageValidatingPolicy for cfg. Every container
// image (init and ephemeral ones too) outside the exemptions must carry a
// signature from at least one trusted key; the pod is rewritten to the digest
// that was verified.
func Policy(cfg *Config) *unstructured.Unstructured {
	names := make([]string, 0, len(cfg.Keys))
	for n := range cfg.Keys {
		names = append(names, n)
	}
	sort.Strings(names)
	var attestors []any
	var refs []string
	for i, n := range names {
		id := fmt.Sprintf("k%d", i) // key names are free-form; CEL identifiers are not
		attestors = append(attestors, map[string]any{
			"name": id,
			"cosign": map[string]any{
				"key":   map[string]any{"data": cfg.Keys[n]},
				"ctlog": map[string]any{"insecureIgnoreTlog": true, "insecureIgnoreSCT": true},
			},
		})
		refs = append(refs, "attestors."+id)
	}
	action := "Audit"
	if cfg.Mode == "enforce" {
		action = "Deny"
	}
	nsExempt := append(append([]string{}, SystemNamespaces...), cfg.ExemptNamespaces...)
	prefixes, _ := json.Marshal(append([]string{}, cfg.ExemptImages...))
	exact, _ := json.Marshal(append([]string{}, cfg.PluginImages...))
	checked := func(v string) string { // CEL: is image v subject to the check?
		return fmt.Sprintf("!(%s in %s) && !%s.exists(p, %s.startsWith(p))", v, exact, prefixes, v)
	}

	spec := map[string]any{
		"validationActions":    []any{action},
		"failurePolicy":        "Fail",
		"webhookConfiguration": map[string]any{"timeoutSeconds": int64(20)},
		"evaluation":           map[string]any{"background": map[string]any{"enabled": true}},
		"matchConstraints": map[string]any{
			"resourceRules": []any{map[string]any{
				"apiGroups": []any{""}, "apiVersions": []any{"v1"},
				"operations": []any{"CREATE", "UPDATE"}, "resources": []any{"pods"},
			}},
			"namespaceSelector": map[string]any{"matchExpressions": []any{
				map[string]any{"key": "kubernetes.io/metadata.name", "operator": "NotIn", "values": toAny(nsExempt)},
				map[string]any{"key": plugin.NamespaceLabel, "operator": "DoesNotExist"},
			}},
		},
		"matchImageReferences": []any{map[string]any{"expression": checked("ref")}},
		"validationConfigurations": map[string]any{
			"mutateDigest": true, "verifyDigest": true, "required": true,
		},
		"attestors": attestors,
		"validations": []any{map[string]any{
			"expression": fmt.Sprintf(
				"[images.containers, images.initContainers, images.ephemeralContainers].all(list, "+
					"list.filter(image, %s).all(image, verifyImageSignatures(image, [%s]) > 0))",
				checked("image"), strings.Join(refs, ", ")),
			"message": "image not signed by a key this cluster trusts (orcinus sign image <image>; orcinus sign status)",
		}},
	}
	creds := map[string]any{}
	if len(cfg.PullSecrets) > 0 {
		creds["secrets"] = toAny(cfg.PullSecrets)
	}
	if cfg.InsecureRegistry {
		creds["allowInsecureRegistry"] = true
	}
	if len(creds) > 0 {
		spec["credentials"] = creds
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "policies.kyverno.io/v1",
		"kind":       "ImageValidatingPolicy",
		"metadata":   map[string]any{"name": policyName},
		"spec":       spec,
	}}
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// Active reports whether the admission check is really in force: the policy
// exists and Kyverno's admission controller has a ready replica (Kyverno
// removes its webhooks when it is shut down).
func Active(ctx context.Context, kubeconfig string) (bool, string) {
	c, err := connect(kubeconfig)
	if err != nil {
		return false, err.Error()
	}
	if _, err := c.dyn.Resource(policyGVR).Get(ctx, policyName, metav1.GetOptions{}); err != nil {
		return false, "policy " + policyName + " not found"
	}
	d, err := c.cs.AppsV1().Deployments(Namespace).Get(ctx, "kyverno-admission-controller", metav1.GetOptions{})
	if err != nil || d.Status.ReadyReplicas == 0 {
		return false, "Kyverno admission controller is not running — pods are NOT being checked"
	}
	return true, ""
}

// Finding is an image that is not signed by a trusted key.
type Finding struct {
	Image  string
	Where  string // namespace/pod or namespace/kind/name
	Reason string
}

func (c *clients) pluginNamespaces(ctx context.Context) (map[string]bool, error) {
	list, err := c.cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: plugin.NamespaceLabel})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, n := range list.Items {
		out[n.Name] = true
	}
	return out, nil
}

// Unsigned checks the images of every running pod outside the exemptions
// against the trusted keys.
func Unsigned(ctx context.Context, kubeconfig string, cfg *Config) ([]Finding, error) {
	c, err := connect(kubeconfig)
	if err != nil {
		return nil, err
	}
	keys, err := cfg.PublicKeys()
	if err != nil {
		return nil, err
	}
	skip, err := c.pluginNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	pods, err := c.cs.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	seen := map[string]error{}
	var out []Finding
	for _, p := range pods.Items {
		if skip[p.Namespace] || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		var images []string
		for _, ct := range append(p.Spec.InitContainers, p.Spec.Containers...) {
			images = append(images, ct.Image)
		}
		for _, img := range images {
			if cfg.Exempt(p.Namespace, img) {
				continue
			}
			verr, done := seen[img]
			if !done {
				_, _, verr = Verify(ctx, img, keys, Options{Insecure: cfg.InsecureRegistry})
				seen[img] = verr
			}
			if verr != nil {
				out = append(out, Finding{Image: img, Where: p.Namespace + "/" + p.Name, Reason: reason(verr)})
			}
		}
	}
	return out, nil
}

func reason(err error) string {
	if errors.Is(err, ErrNotSigned) {
		return "not signed by a trusted key"
	}
	return err.Error()
}

// Preflight runs before a deploy when signing is on: it verifies every image
// of the objects outside the exemptions and pins it to the verified digest,
// so what runs is exactly what was checked. In enforce mode an unsigned image
// fails the deploy here, with a clearer message than the admission webhook's;
// in audit mode it is returned as a warning. With signing off it does nothing.
func Preflight(ctx context.Context, kubeconfig, defaultNamespace string, objects []runtime.Object) (warnings []string, err error) {
	c, err := connect(kubeconfig)
	if err != nil {
		return nil, err
	}
	cfg, err := c.load(ctx)
	if apierrors.IsForbidden(err) {
		return nil, nil // can't read the settings; the admission policy still checks
	}
	if err != nil || cfg == nil || cfg.Mode == "off" {
		return nil, err
	}
	keys, err := cfg.PublicKeys()
	if err != nil {
		return nil, err
	}
	skip, err := c.pluginNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, o := range objects {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return nil, err
		}
		obj := unstructured.Unstructured{Object: u}
		ns := obj.GetNamespace()
		if ns == "" {
			ns = defaultNamespace
		}
		if ns == "" {
			ns = "default"
		}
		if skip[ns] {
			continue
		}
		changed := false
		for _, cs := range deploy.ContainerLists(&obj) {
			for _, ct := range cs {
				m, ok := ct.(map[string]any)
				img, _ := m["image"].(string)
				if !ok || img == "" || cfg.Exempt(ns, img) {
					continue
				}
				d, _, err := Verify(ctx, img, keys, Options{Insecure: cfg.InsecureRegistry})
				if err != nil {
					bad = append(bad, fmt.Sprintf("%s/%s: %s: %s", obj.GetKind(), obj.GetName(), img, reason(err)))
					continue
				}
				m["image"], changed = d.String(), true
			}
		}
		if !changed {
			continue
		}
		if uo, ok := o.(*unstructured.Unstructured); ok {
			uo.Object = obj.Object
		} else if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, o); err != nil {
			return nil, err
		}
	}
	if len(bad) == 0 {
		return nil, nil
	}
	if cfg.Mode == "enforce" {
		return nil, fmt.Errorf("image signing is enforced and these images are not signed by a trusted key:\n  %s\n"+
			"sign them with `orcinus sign image <image>` (or exempt them: orcinus sign exempt add <image>)", strings.Join(bad, "\n  "))
	}
	return bad, nil
}

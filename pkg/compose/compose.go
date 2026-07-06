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
	"strings"

	"github.com/kubernetes/kompose/pkg/kobject"
	"github.com/kubernetes/kompose/pkg/loader"
	"github.com/kubernetes/kompose/pkg/transformer/kubernetes"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	ingressCfgs := map[string]ingressCfg{}
	autoscaleCfgs := map[string]autoscaleCfg{}
	strategyCfgs := map[string]strategyCfg{}
	rolloutCfgs := map[string]string{}
	pullSecrets := map[string][]string{}
	bindMounts := map[string][]bindMount{}
	placements := map[string]placementCfg{}
	nodeSelectors := map[string]map[string]string{}
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
		for svc, cfg := range pp.ingress {
			ingressCfgs[svc] = cfg
		}
		for svc, cfg := range pp.autoscale {
			autoscaleCfgs[svc] = cfg
		}
		for svc, cfg := range pp.strategy {
			strategyCfgs[svc] = cfg
		}
		for svc, kind := range pp.rollout {
			rolloutCfgs[svc] = kind
		}
		for svc, names := range pp.imagePullSecrets {
			pullSecrets[svc] = names
		}
		for svc, mounts := range pp.bindMounts {
			bindMounts[svc] = mounts
		}
		for svc, pc := range pp.placement {
			placements[svc] = pc
		}
		for svc, sel := range pp.nodeSelector {
			nodeSelectors[svc] = sel
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

	// 6. Apply ingress hints (TLS/path/port/class/Traefik middlewares) to the
	//    generated Ingresses; may generate Traefik Middleware CRDs.
	objects = append(objects, applyIngressConfig(objects, ingressCfgs, opts)...)

	// 6b. Attach private-registry imagePullSecrets to workloads (before Rollout
	//     conversion so Rollouts inherit them from the Deployment template).
	applyImagePullSecrets(objects, pullSecrets)

	// 6c. Attach host-path (bind-mount) volumes — node-local, like a Compose/Swarm
	//     bind mount (before Rollout conversion so Rollouts inherit them).
	applyBindMounts(objects, bindMounts)

	// 6d. Swarm deploy.placement → nodeAffinity/topologySpread, plus the plain
	//     x-orcinus-node-selector (before Rollout conversion so Rollouts inherit).
	applyPlacement(objects, placements)
	applyNodeSelector(objects, nodeSelectors)

	// 7. Convert Deployments to Argo Rollouts for x-orcinus-rollout services.
	objects, rolloutSvcs, err := convertRollouts(objects, rolloutCfgs)
	if err != nil {
		return nil, err
	}

	// 8. Generate HorizontalPodAutoscalers from x-orcinus-autoscale-*.
	hpas := buildAutoscalers(objects, autoscaleCfgs, opts, rolloutSvcs)
	objects = append(objects, hpas...)

	// 9. Apply Deployment update strategy from x-orcinus-strategy (Rollouts skip).
	applyStrategy(objects, strategyCfgs)

	sortObjects(objects)
	return objects, nil
}

// convertRollouts replaces the Deployment of each x-orcinus-rollout service with
// an Argo Rollout CR (canary or blue-green). Returns the new object set and the
// set of services that became Rollouts.
func convertRollouts(objects []runtime.Object, cfgs map[string]string) ([]runtime.Object, map[string]bool, error) {
	if len(cfgs) == 0 {
		return objects, nil, nil
	}
	hasService := map[string]bool{}
	for _, o := range objects {
		if s, ok := o.(*corev1.Service); ok {
			hasService[s.Name] = true
		}
	}

	rolloutSvcs := map[string]bool{}
	out := make([]runtime.Object, 0, len(objects))
	for _, o := range objects {
		dep, ok := o.(*appsv1.Deployment)
		if !ok {
			out = append(out, o)
			continue
		}
		kind, want := cfgs[dep.Name]
		if !want {
			out = append(out, o)
			continue
		}
		ro, err := deploymentToRollout(dep, kind, hasService[dep.Name])
		if err != nil {
			return nil, nil, err
		}
		out = append(out, ro)
		rolloutSvcs[dep.Name] = true
	}
	return out, rolloutSvcs, nil
}

// deploymentToRollout builds an Argo Rollout (unstructured) from a Deployment.
func deploymentToRollout(dep *appsv1.Deployment, kind string, hasService bool) (runtime.Object, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(dep)
	if err != nil {
		return nil, err
	}
	spec, _ := m["spec"].(map[string]interface{})
	if spec == nil {
		return nil, fmt.Errorf("rollout %q: deployment has no spec", dep.Name)
	}
	// The Rollout CRD's structural schema rejects the null template
	// creationTimestamp that typed→unstructured conversion leaves behind.
	if tmpl, ok := spec["template"].(map[string]interface{}); ok {
		if md, ok := tmpl["metadata"].(map[string]interface{}); ok {
			delete(md, "creationTimestamp")
		}
	}

	var strategy map[string]interface{}
	switch kind {
	case "canary":
		strategy = map[string]interface{}{"canary": map[string]interface{}{
			"steps": []interface{}{
				map[string]interface{}{"setWeight": int64(50)},
				map[string]interface{}{"pause": map[string]interface{}{"duration": int64(15)}},
			},
		}}
	case "bluegreen":
		if !hasService {
			return nil, fmt.Errorf("rollout %q: blue-green needs a Service (add a `ports:` entry)", dep.Name)
		}
		strategy = map[string]interface{}{"blueGreen": map[string]interface{}{
			"activeService":        dep.Name,
			"autoPromotionEnabled": true,
		}}
	default:
		return nil, fmt.Errorf("rollout %q: unknown kind %q", dep.Name, kind)
	}

	rolloutSpec := map[string]interface{}{
		"replicas": spec["replicas"],
		"selector": spec["selector"],
		"template": spec["template"],
		"strategy": strategy,
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Rollout",
		"metadata": map[string]interface{}{
			"name":      dep.Name,
			"namespace": dep.Namespace,
			"labels":    toStringMapIface(dep.Labels),
		},
		"spec": rolloutSpec,
	}}, nil
}

func toStringMapIface(m map[string]string) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// applyStrategy sets the update strategy (rolling/recreate + knobs) on the
// Deployment for each service that requested one.
func applyStrategy(objects []runtime.Object, cfgs map[string]strategyCfg) {
	if len(cfgs) == 0 {
		return
	}
	for _, obj := range objects {
		dep, ok := obj.(*appsv1.Deployment)
		if !ok {
			continue
		}
		cfg, ok := cfgs[dep.Name]
		if !ok {
			continue
		}
		if cfg.MinReadySeconds > 0 {
			dep.Spec.MinReadySeconds = cfg.MinReadySeconds
		}
		if cfg.ProgressDeadline > 0 {
			pd := cfg.ProgressDeadline
			dep.Spec.ProgressDeadlineSeconds = &pd
		}
		if cfg.Type == "recreate" {
			dep.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
			continue
		}
		// rolling (default) — set knobs if provided.
		ru := &appsv1.RollingUpdateDeployment{}
		set := false
		if cfg.MaxSurge != "" {
			v := intstr.Parse(cfg.MaxSurge)
			ru.MaxSurge = &v
			set = true
		}
		if cfg.MaxUnavailable != "" {
			v := intstr.Parse(cfg.MaxUnavailable)
			ru.MaxUnavailable = &v
			set = true
		}
		strat := appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
		if set {
			strat.RollingUpdate = ru
		}
		dep.Spec.Strategy = strat
	}
}

// buildAutoscalers creates an HPA per service that requested autoscaling,
// targeting that service's Deployment or StatefulSet.
func buildAutoscalers(objects []runtime.Object, cfgs map[string]autoscaleCfg, opts Options, rolloutSvcs map[string]bool) []runtime.Object {
	if len(cfgs) == 0 {
		return nil
	}
	// Map service name → workload kind that was generated.
	kindOf := map[string]string{}
	for _, obj := range objects {
		switch t := obj.(type) {
		case *appsv1.Deployment:
			kindOf[t.Name] = "Deployment"
		case *appsv1.StatefulSet:
			if _, ok := kindOf[t.Name]; !ok {
				kindOf[t.Name] = "StatefulSet"
			}
		}
	}

	var out []runtime.Object
	for svc, cfg := range cfgs {
		apiVersion := "apps/v1"
		kind := kindOf[svc]
		if kind == "" {
			kind = "Deployment"
		}
		if rolloutSvcs[svc] { // HPA targets the Rollout instead
			apiVersion, kind = "argoproj.io/v1alpha1", "Rollout"
		}
		min := int32(cfg.Min)
		if min < 1 {
			min = 1
		}
		cpu := int32(cfg.CPU)
		mem := int32(cfg.Memory)
		if cpu == 0 && mem == 0 {
			cpu = 80
		}
		hpa := &autoscalingv2.HorizontalPodAutoscaler{
			TypeMeta:   metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
			ObjectMeta: metav1.ObjectMeta{Name: svc},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: apiVersion, Kind: kind, Name: svc},
				MinReplicas:    &min,
				MaxReplicas:    int32(cfg.Max),
				Metrics:        autoscaleMetrics(cpu, mem),
			},
		}
		out = append(out, hpa)
	}
	decorate(out, opts.ProjectName, opts.Namespace)
	return out
}

func autoscaleMetrics(cpu, mem int32) []autoscalingv2.MetricSpec {
	var m []autoscalingv2.MetricSpec
	add := func(res corev1.ResourceName, v int32) {
		t := v
		m = append(m, autoscalingv2.MetricSpec{
			Type: autoscalingv2.ResourceMetricSourceType,
			Resource: &autoscalingv2.ResourceMetricSource{
				Name:   res,
				Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &t},
			},
		})
	}
	if cpu > 0 {
		add(corev1.ResourceCPU, cpu)
	}
	if mem > 0 {
		add(corev1.ResourceMemory, mem)
	}
	return m
}

// applyIngressConfig enriches each generated Ingress with the service's
// x-orcinus ingress hints: cert-manager TLS, ingress class, path, backend port,
// and Traefik middlewares (StripPrefix + attach-by-name). It returns any Traefik
// Middleware CRDs it generated (e.g. the auto StripPrefix), to be applied too.
func applyIngressConfig(objects []runtime.Object, cfgs map[string]ingressCfg, opts Options) []runtime.Object {
	if len(cfgs) == 0 {
		return nil
	}
	var generated []runtime.Object
	for _, obj := range objects {
		ing, ok := obj.(*networkingv1.Ingress)
		if !ok {
			continue
		}
		cfg, ok := cfgs[ing.Name] // kompose names the Ingress after the service
		if !ok {
			continue
		}
		if cfg.Class != "" {
			c := cfg.Class
			ing.Spec.IngressClassName = &c
		}
		switch {
		case cfg.TLSSecret != "":
			// Custom/BYO cert: use an existing TLS Secret, no cert-manager.
			ing.Spec.TLS = []networkingv1.IngressTLS{{
				Hosts:      allIngressHosts(ing),
				SecretName: cfg.TLSSecret,
			}}
		case cfg.TLS != "":
			if ing.Annotations == nil {
				ing.Annotations = map[string]string{}
			}
			ing.Annotations["cert-manager.io/cluster-issuer"] = cfg.TLS
			ing.Spec.TLS = []networkingv1.IngressTLS{{
				Hosts:      allIngressHosts(ing),
				SecretName: ing.Name + "-tls",
			}}
		}
		for ri := range ing.Spec.Rules {
			rule := &ing.Spec.Rules[ri]
			if rule.HTTP == nil {
				continue
			}
			for pi := range rule.HTTP.Paths {
				p := &rule.HTTP.Paths[pi]
				if cfg.Path != "" {
					p.Path = cfg.Path
				}
				if cfg.Port != 0 && p.Backend.Service != nil {
					p.Backend.Service.Port = networkingv1.ServiceBackendPort{Number: int32(cfg.Port)}
				}
			}
		}

		// Traefik middlewares (Traefik is the runtime's native ingress controller).
		ns := opts.Namespace
		if ns == "" {
			ns = "default"
		}
		var refs []string
		// StripPrefix runs first so downstream middlewares/apps see the stripped path.
		if prefixes := stripPrefixesFor(cfg, ing); len(prefixes) > 0 {
			name := ing.Name + "-stripprefix"
			generated = append(generated, traefikStripPrefix(name, ns, ownershipLabels(opts.ProjectName), prefixes))
			refs = append(refs, fmt.Sprintf("%s-%s@kubernetescrd", ns, name))
		}
		for _, m := range cfg.Middlewares {
			refs = append(refs, fmt.Sprintf("%s-%s@kubernetescrd", ns, m))
		}
		if len(refs) > 0 {
			if ing.Annotations == nil {
				ing.Annotations = map[string]string{}
			}
			ing.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = strings.Join(refs, ",")
		}
	}
	return generated
}

// stripPrefixesFor resolves the prefixes to strip: explicit x-orcinus-strip-prefix
// values, or (when set to true) the ingress path prefixes (excluding a bare "/").
func stripPrefixesFor(cfg ingressCfg, ing *networkingv1.Ingress) []string {
	if len(cfg.StripPrefixes) > 0 {
		return cfg.StripPrefixes
	}
	if !cfg.StripFromPath {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			if p.Path == "" || p.Path == "/" || seen[p.Path] {
				continue
			}
			seen[p.Path] = true
			out = append(out, p.Path)
		}
	}
	return out
}

// traefikStripPrefix builds a Traefik StripPrefix Middleware CRD (traefik.io/v1alpha1).
func traefikStripPrefix(name, namespace string, labels map[string]string, prefixes []string) runtime.Object {
	ps := make([]interface{}, len(prefixes))
	for i, p := range prefixes {
		ps[i] = p
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "traefik.io/v1alpha1",
		"kind":       "Middleware",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
			"labels":    toStringMapIface(labels),
		},
		"spec": map[string]interface{}{
			"stripPrefix": map[string]interface{}{"prefixes": ps},
		},
	}}
}

// ownershipLabels returns the standard orcinus ownership labels for generated objects.
func ownershipLabels(project string) map[string]string {
	l := map[string]string{LabelManagedBy: ManagedByValue}
	if project != "" {
		l[LabelPartOf] = project
		l[LabelProject] = project
	}
	return l
}

// allIngressHosts returns every distinct host across the Ingress rules, so a
// multi-domain Ingress (x-orcinus-host: "a,b,c") gets a TLS cert covering all of
// them, not just the first.
func allIngressHosts(ing *networkingv1.Ingress) []string {
	var hosts []string
	seen := map[string]bool{}
	for _, r := range ing.Spec.Rules {
		if r.Host != "" && !seen[r.Host] {
			seen[r.Host] = true
			hosts = append(hosts, r.Host)
		}
	}
	return hosts
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

		// Also stamp the pod template so the resulting Pods carry ownership
		// labels (used by `orcinus ps`). This only adds template labels; the
		// immutable selector is left untouched.
		if tmpl := templateMetaOf(obj); tmpl != nil {
			tl := tmpl.GetLabels()
			if tl == nil {
				tl = map[string]string{}
			}
			tl[LabelManagedBy] = ManagedByValue
			if project != "" {
				tl[LabelPartOf] = project
				tl[LabelProject] = project
			}
			tmpl.SetLabels(tl)
		}
	}
}

// templateMetaOf returns the pod-template ObjectMeta for supported controllers.
func templateMetaOf(obj runtime.Object) *metav1.ObjectMeta {
	switch t := obj.(type) {
	case *appsv1.Deployment:
		return &t.Spec.Template.ObjectMeta
	case *appsv1.StatefulSet:
		return &t.Spec.Template.ObjectMeta
	case *appsv1.DaemonSet:
		return &t.Spec.Template.ObjectMeta
	}
	return nil
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
// applyImagePullSecrets adds imagePullSecrets to each workload's pod template for
// services that named a private-registry secret via x-orcinus-image-pull-secret.
func applyImagePullSecrets(objects []runtime.Object, cfgs map[string][]string) {
	if len(cfgs) == 0 {
		return
	}
	for _, obj := range objects {
		ps, name := podSpecOf(obj)
		if ps == nil {
			continue
		}
		names, ok := cfgs[name]
		if !ok {
			continue
		}
		have := map[string]bool{}
		for _, r := range ps.ImagePullSecrets {
			have[r.Name] = true
		}
		for _, n := range names {
			if !have[n] {
				ps.ImagePullSecrets = append(ps.ImagePullSecrets, corev1.LocalObjectReference{Name: n})
			}
		}
	}
}

// applyBindMounts adds hostPath volumes + volumeMounts for each service's
// bind mounts. hostPath is node-local (like a Compose/Swarm bind mount): the
// path lives on whichever node the pod runs on.
func applyBindMounts(objects []runtime.Object, cfgs map[string][]bindMount) {
	if len(cfgs) == 0 {
		return
	}
	hostPathType := corev1.HostPathDirectoryOrCreate
	for _, obj := range objects {
		ps, name := podSpecOf(obj)
		if ps == nil {
			continue
		}
		for _, bm := range cfgs[name] {
			ps.Volumes = append(ps.Volumes, corev1.Volume{
				Name: bm.Name,
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: bm.Source, Type: &hostPathType},
				},
			})
			for ci := range ps.Containers {
				ps.Containers[ci].VolumeMounts = append(ps.Containers[ci].VolumeMounts,
					corev1.VolumeMount{Name: bm.Name, MountPath: bm.Target, ReadOnly: bm.ReadOnly})
			}
		}
	}
}

// applyPlacement maps Swarm placement to Kubernetes scheduling: constraints →
// required nodeAffinity, preferences(spread) → topologySpreadConstraints.
func applyPlacement(objects []runtime.Object, cfgs map[string]placementCfg) {
	if len(cfgs) == 0 {
		return
	}
	for _, obj := range objects {
		ps, name := podSpecOf(obj)
		if ps == nil {
			continue
		}
		cfg, ok := cfgs[name]
		if !ok {
			continue
		}
		if len(cfg.constraints) > 0 {
			reqs := make([]corev1.NodeSelectorRequirement, 0, len(cfg.constraints))
			for _, c := range cfg.constraints {
				reqs = append(reqs, corev1.NodeSelectorRequirement{
					Key: c.Key, Operator: corev1.NodeSelectorOperator(c.Operator), Values: c.Values,
				})
			}
			if ps.Affinity == nil {
				ps.Affinity = &corev1.Affinity{}
			}
			if ps.Affinity.NodeAffinity == nil {
				ps.Affinity.NodeAffinity = &corev1.NodeAffinity{}
			}
			na := ps.Affinity.NodeAffinity
			if na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
				na.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{}},
				}
			}
			sel := na.RequiredDuringSchedulingIgnoredDuringExecution
			if len(sel.NodeSelectorTerms) == 0 {
				sel.NodeSelectorTerms = []corev1.NodeSelectorTerm{{}}
			}
			sel.NodeSelectorTerms[0].MatchExpressions = append(sel.NodeSelectorTerms[0].MatchExpressions, reqs...)
		}
		for _, key := range cfg.spreadKeys {
			ps.TopologySpreadConstraints = append(ps.TopologySpreadConstraints, corev1.TopologySpreadConstraint{
				MaxSkew:           1,
				TopologyKey:       key,
				WhenUnsatisfiable: corev1.ScheduleAnyway,
				LabelSelector:     &metav1.LabelSelector{MatchLabels: workloadSelector(obj)},
			})
		}
	}
}

// applyNodeSelector sets a plain pod nodeSelector from x-orcinus-node-selector.
func applyNodeSelector(objects []runtime.Object, cfgs map[string]map[string]string) {
	if len(cfgs) == 0 {
		return
	}
	for _, obj := range objects {
		ps, name := podSpecOf(obj)
		if ps == nil {
			continue
		}
		sel := cfgs[name]
		if len(sel) == 0 {
			continue
		}
		if ps.NodeSelector == nil {
			ps.NodeSelector = map[string]string{}
		}
		for k, v := range sel {
			ps.NodeSelector[k] = v
		}
	}
}

// workloadSelector returns a workload's selector match labels (for topologySpread).
func workloadSelector(obj runtime.Object) map[string]string {
	switch t := obj.(type) {
	case *appsv1.Deployment:
		if t.Spec.Selector != nil {
			return t.Spec.Selector.MatchLabels
		}
	case *appsv1.StatefulSet:
		if t.Spec.Selector != nil {
			return t.Spec.Selector.MatchLabels
		}
	case *appsv1.DaemonSet:
		if t.Spec.Selector != nil {
			return t.Spec.Selector.MatchLabels
		}
	}
	return nil
}

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

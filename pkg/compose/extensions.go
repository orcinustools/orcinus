package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// x-orcinus-* extension keys (ARCHITECTURE.md §8). Orcinus owns this schema and
// translates it onto the forked kompose engine's native per-service labels so the
// fork does the heavy lifting while orcinus keeps a clean, stable surface.
const (
	extController = "x-orcinus-controller" // deployment | statefulset | daemonset
	extExpose     = "x-orcinus-expose"     // ingress | nodeport | loadbalancer | clusterip
	extHost       = "x-orcinus-host"       // ingress host (used with expose: ingress)
	extVolumeSize = "x-orcinus-volume-size"
	extSecret     = "x-orcinus-secret" // list (or scalar) of env var names to store in a Secret

	extTLS          = "x-orcinus-tls"           // cert-manager ClusterIssuer name (e.g. "letsencrypt")
	extTLSSecret    = "x-orcinus-tls-secret"    // existing TLS Secret (custom/BYO cert)
	extPath         = "x-orcinus-path"          // ingress path (default "/")
	extPort         = "x-orcinus-port"          // service port the ingress routes to
	extIngressClass = "x-orcinus-ingress-class" // ingress class (e.g. traefik, nginx)

	extStripPrefix = "x-orcinus-strip-prefix" // Traefik StripPrefix: true (strip the path) | prefix | list of prefixes
	extMiddleware  = "x-orcinus-middleware"   // Traefik middleware name(s) to attach to the ingress route (in order)

	extImagePullSecret = "x-orcinus-image-pull-secret" // imagePullSecret name(s) for a private registry
	extNodeSelector    = "x-orcinus-node-selector"     // map of node labels to pin the pod (k8s nodeSelector)

	extAutoscaleMin = "x-orcinus-autoscale-min"    // HPA min replicas
	extAutoscaleMax = "x-orcinus-autoscale-max"    // HPA max replicas (enables HPA)
	extAutoscaleCPU = "x-orcinus-autoscale-cpu"    // HPA target CPU %
	extAutoscaleMem = "x-orcinus-autoscale-memory" // HPA target memory %

	extStrategy       = "x-orcinus-strategy"        // rolling | recreate
	extMaxSurge       = "x-orcinus-max-surge"       // rolling: e.g. 25% or 1
	extMaxUnavailable = "x-orcinus-max-unavailable" // rolling: e.g. 0 or 25%

	extRollout = "x-orcinus-rollout" // canary | bluegreen (Argo Rollout)
)

// kompose native label keys we translate onto.
const (
	lblController = "kompose.controller.type"
	lblServiceTy  = "kompose.service.type"
	lblExpose     = "kompose.service.expose"
	lblVolumeSize = "kompose.volume.size"
)

// ingressCfg holds ingress hints applied to a service's Ingress after transform.
type ingressCfg struct {
	TLS       string // ClusterIssuer name; enables cert-manager TLS when non-empty
	TLSSecret string // existing TLS Secret name (custom/BYO cert); wins over TLS
	Path      string
	Port      int
	Class     string

	// Traefik middleware wiring (Traefik is the runtime's native ingress).
	StripFromPath bool     // strip-prefix: true → auto-strip the ingress path prefix
	StripPrefixes []string // explicit prefixes to strip (generates a StripPrefix Middleware)
	Middlewares   []string // existing Traefik middleware names to attach (in order)
}

// isSet reports whether any ingress hint was provided.
func (c ingressCfg) isSet() bool {
	return c.TLS != "" || c.TLSSecret != "" || c.Path != "" || c.Class != "" || c.Port != 0 ||
		c.StripFromPath || len(c.StripPrefixes) > 0 || len(c.Middlewares) > 0
}

// bindMount is a host-path (bind) volume extracted from a service's `volumes:`.
// It maps to a Kubernetes hostPath volume (node-local, like a Compose/Swarm bind
// mount), instead of the PVC that named volumes become.
type bindMount struct {
	Name     string
	Source   string // absolute host path on the node
	Target   string // mount path in the container
	ReadOnly bool
}

// nodeConstraint is one Swarm placement constraint mapped to a Kubernetes node
// selector requirement (kept provider-neutral so extensions.go needs no k8s import).
type nodeConstraint struct {
	Key      string
	Operator string // In | NotIn | Exists | DoesNotExist
	Values   []string
}

// placementCfg holds a service's Swarm `deploy.placement` mapped for Kubernetes:
// constraints → nodeAffinity, preferences(spread) → topologySpreadConstraints.
type placementCfg struct {
	constraints []nodeConstraint
	spreadKeys  []string
}

// autoscaleCfg holds HPA hints for a service.
type autoscaleCfg struct {
	Min, Max, CPU, Memory int
}

// strategyCfg holds Deployment update-strategy hints for a service, sourced from
// the standard compose `deploy.update_config` and/or `x-orcinus-*` overrides.
type strategyCfg struct {
	Type             string // rolling | recreate
	MaxSurge         string
	MaxUnavailable   string
	MinReadySeconds  int32 // from update_config.delay
	ProgressDeadline int32 // from update_config.monitor
}

// preprocessed is the result of translating one compose document.
type preprocessed struct {
	// content is the rewritten compose bytes (with kompose.* labels injected).
	content []byte
	// secrets maps a service name to env var names that should be stored in a
	// Secret. Handled after transform since kompose has no direct equivalent.
	secrets map[string][]string
	// ingress maps a service name to ingress hints applied after transform.
	ingress map[string]ingressCfg
	// autoscale maps a service name to HPA hints applied after transform.
	autoscale map[string]autoscaleCfg
	// strategy maps a service name to Deployment update-strategy hints.
	strategy map[string]strategyCfg
	// rollout maps a service name to an Argo Rollout kind (canary|bluegreen).
	rollout map[string]string
	// imagePullSecrets maps a service name to imagePullSecret names (private registry).
	imagePullSecrets map[string][]string
	// bindMounts maps a service name to host-path (bind) volumes → hostPath.
	bindMounts map[string][]bindMount
	// placement maps a service name to Swarm placement → nodeAffinity/topologySpread.
	placement map[string]placementCfg
	// nodeSelector maps a service name to a plain nodeSelector (x-orcinus-node-selector).
	nodeSelector map[string]map[string]string
	// gpu maps a service name to extended-resource limits (e.g. nvidia.com/gpu: "1")
	// from deploy.resources.reservations.generic_resources.
	gpu map[string]map[string]string
}

// injectKomposeLabels reads x-orcinus-* keys from every service and rewrites the
// compose document so the forked kompose engine sees equivalent native labels.
// baseDir is the compose file's directory (to resolve relative bind-mount /
// config / secret file paths); activeProfiles filters services by compose profile.
func injectKomposeLabels(composeBytes []byte, baseDir string, activeProfiles []string) (*preprocessed, error) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose document: %w", err)
	}

	out := &preprocessed{
		secrets:          map[string][]string{},
		ingress:          map[string]ingressCfg{},
		autoscale:        map[string]autoscaleCfg{},
		strategy:         map[string]strategyCfg{},
		rollout:          map[string]string{},
		imagePullSecrets: map[string][]string{},
		bindMounts:       map[string][]bindMount{},
		placement:        map[string]placementCfg{},
		nodeSelector:     map[string]map[string]string{},
		gpu:              map[string]map[string]string{},
	}

	// Resolve relative file paths in top-level configs:/secrets: to absolute, so
	// they still resolve after the doc is copied to a temp dir for the fork.
	resolveFileRefs(doc["configs"], baseDir)
	resolveFileRefs(doc["secrets"], baseDir)

	servicesAny, ok := doc["services"].(map[string]interface{})
	if !ok {
		// No services or unexpected shape: pass through unchanged.
		out.content = composeBytes
		return out, nil
	}

	for name, svcAny := range servicesAny {
		svc, ok := svcAny.(map[string]interface{})
		if !ok {
			continue
		}
		// compose profiles: drop services not selected by the active profile(s);
		// for selected ones strip `profiles:` so the fork's loader doesn't re-filter.
		if !serviceInProfiles(svc, activeProfiles) {
			delete(servicesAny, name)
			continue
		}
		delete(svc, "profiles")
		labels := normalizeLabels(svc["labels"])

		if v, ok := stringExt(svc[extController]); ok {
			labels[lblController] = v
		}
		if secretNames := stringSliceExt(svc[extSecret]); len(secretNames) > 0 {
			out.secrets[name] = secretNames
		}
		if v, ok := stringExt(svc[extVolumeSize]); ok {
			labels[lblVolumeSize] = v
		}

		if expose, ok := stringExt(svc[extExpose]); ok {
			switch expose {
			case "ingress":
				if host, ok := stringExt(svc[extHost]); ok {
					labels[lblExpose] = host
				} else {
					labels[lblExpose] = "true"
				}
			case "nodeport", "loadbalancer", "clusterip", "headless":
				labels[lblServiceTy] = expose
			default:
				return nil, fmt.Errorf("service %q: invalid %s=%q", name, extExpose, expose)
			}
		}

		// Ingress hints applied after transform (TLS/path/port/class).
		var ic ingressCfg
		if v, ok := stringExt(svc[extTLS]); ok {
			ic.TLS = v
		}
		if v, ok := stringExt(svc[extTLSSecret]); ok {
			ic.TLSSecret = v
		}
		if v, ok := stringExt(svc[extPath]); ok {
			ic.Path = v
		}
		if v, ok := stringExt(svc[extIngressClass]); ok {
			ic.Class = v
		}
		if v, ok := intExt(svc[extPort]); ok {
			ic.Port = v
		}
		// Traefik StripPrefix: true → strip the ingress path; string/list → strip those prefixes.
		switch t := svc[extStripPrefix].(type) {
		case bool:
			ic.StripFromPath = t
		case string:
			if t != "" {
				ic.StripPrefixes = []string{t}
			}
		case []interface{}:
			ic.StripPrefixes = stringSliceExt(t)
		}
		if mws := stringSliceExt(svc[extMiddleware]); len(mws) > 0 {
			ic.Middlewares = mws
		}
		if ic.isSet() {
			out.ingress[name] = ic
		}

		// Autoscale hints → HPA after transform.
		var ac autoscaleCfg
		if v, ok := intExt(svc[extAutoscaleMin]); ok {
			ac.Min = v
		}
		if v, ok := intExt(svc[extAutoscaleMax]); ok {
			ac.Max = v
		}
		if v, ok := intExt(svc[extAutoscaleCPU]); ok {
			ac.CPU = v
		}
		if v, ok := intExt(svc[extAutoscaleMem]); ok {
			ac.Memory = v
		}
		if ac.Max > 0 {
			out.autoscale[name] = ac
		}

		// Deployment update strategy: standard compose `deploy.update_config`
		// first, then x-orcinus-* overrides.
		var sc strategyCfg
		parseUpdateConfig(svc, &sc)
		if v, ok := stringExt(svc[extStrategy]); ok {
			if v != "rolling" && v != "recreate" {
				return nil, fmt.Errorf("service %q: invalid %s=%q (want rolling|recreate)", name, extStrategy, v)
			}
			sc.Type = v
		}
		if v, ok := stringExt(svc[extMaxSurge]); ok {
			sc.MaxSurge = v
		}
		if v, ok := stringExt(svc[extMaxUnavailable]); ok {
			sc.MaxUnavailable = v
		}
		if sc != (strategyCfg{}) {
			out.strategy[name] = sc
		}

		// Private-registry pull secrets → pod imagePullSecrets.
		if secrets := stringSliceExt(svc[extImagePullSecret]); len(secrets) > 0 {
			out.imagePullSecrets[name] = secrets
		}

		// x-orcinus-node-selector → plain pod nodeSelector.
		if sel := stringMapExt(svc[extNodeSelector]); len(sel) > 0 {
			out.nodeSelector[name] = sel
		}

		// deploy.endpoint_mode: dnsrr → headless Service (unless a type is set).
		// deploy.resources.reservations.generic_resources → GPU/extended limits.
		if deploy, ok := svc["deploy"].(map[string]interface{}); ok {
			if em, _ := stringExt(deploy["endpoint_mode"]); em == "dnsrr" {
				if _, set := labels[lblServiceTy]; !set {
					labels[lblServiceTy] = "headless"
				}
			}
			if g := parseGenericResources(deploy); len(g) > 0 {
				out.gpu[name] = g
			}
		}

		// Swarm deploy.placement → nodeAffinity + topologySpread. Parsed here and
		// stripped from the doc so the fork doesn't half-map it.
		pc, err := parsePlacement(svc)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		if pc != nil {
			out.placement[name] = *pc
			stripPlacement(svc)
		}

		// Bind mounts (host path → container) become hostPath volumes; named
		// volumes are left for the fork to turn into PVCs. Strip the bind mounts
		// from the compose doc so the fork doesn't PVC them.
		if mounts, kept := extractBindMounts(svc["volumes"], baseDir); len(mounts) > 0 {
			out.bindMounts[name] = mounts
			if len(kept) == 0 {
				delete(svc, "volumes")
			} else {
				svc["volumes"] = kept
			}
		}

		// Progressive delivery via Argo Rollout.
		if v, ok := stringExt(svc[extRollout]); ok {
			if v != "canary" && v != "bluegreen" {
				return nil, fmt.Errorf("service %q: invalid %s=%q (want canary|bluegreen)", name, extRollout, v)
			}
			out.rollout[name] = v
		}

		if len(labels) > 0 {
			svc["labels"] = labels
		}
	}

	content, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("re-marshal compose document: %w", err)
	}
	out.content = content
	return out, nil
}

// resolveFileRefs rewrites relative `file:` paths in a top-level configs:/secrets:
// map to absolute (relative to the compose file's dir), so they still resolve
// after the doc is copied to a temp dir for the fork loader.
func resolveFileRefs(v interface{}, baseDir string) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return
	}
	for _, entryAny := range m {
		entry, ok := entryAny.(map[string]interface{})
		if !ok {
			continue
		}
		if f, ok := entry["file"].(string); ok && f != "" && !filepath.IsAbs(f) {
			entry["file"] = absPath(f, baseDir)
		}
	}
}

// serviceInProfiles reports whether a service is selected given the active
// profiles. A service with no `profiles:` is always selected; one with profiles
// is selected only if it shares at least one with the active set.
func serviceInProfiles(svc map[string]interface{}, active []string) bool {
	profs := stringSliceExt(svc["profiles"])
	if len(profs) == 0 {
		return true
	}
	for _, p := range profs {
		for _, a := range active {
			if p == a {
				return true
			}
		}
	}
	return false
}

// parseGenericResources maps deploy.resources.reservations.generic_resources to
// Kubernetes extended-resource limits (a "gpu" kind → nvidia.com/gpu).
func parseGenericResources(deploy map[string]interface{}) map[string]string {
	res, ok := deploy["resources"].(map[string]interface{})
	if !ok {
		return nil
	}
	rsv, ok := res["reservations"].(map[string]interface{})
	if !ok {
		return nil
	}
	list, ok := rsv["generic_resources"].([]interface{})
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		spec, ok := m["discrete_resource_spec"].(map[string]interface{})
		if !ok {
			continue
		}
		kind, _ := stringExt(spec["kind"])
		val, _ := stringExt(spec["value"])
		if kind == "" || val == "" {
			continue
		}
		name := kind
		if !strings.Contains(kind, "/") && strings.Contains(strings.ToLower(kind), "gpu") {
			name = "nvidia.com/gpu" // common case: "gpu" / "NVIDIA-GPU" → nvidia.com/gpu
		}
		out[name] = val
	}
	return out
}

// parsePlacement maps a service's Swarm `deploy.placement` to a placementCfg, or
// returns nil if there is none. Unknown constraint keys are a hard error.
func parsePlacement(svc map[string]interface{}) (*placementCfg, error) {
	deploy, ok := svc["deploy"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	pl, ok := deploy["placement"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	cfg := &placementCfg{}
	if cs, ok := pl["constraints"].([]interface{}); ok {
		for _, c := range cs {
			s, ok := c.(string)
			if !ok {
				continue
			}
			nc, err := parseConstraint(s)
			if err != nil {
				return nil, err
			}
			cfg.constraints = append(cfg.constraints, nc)
		}
	}
	if prefs, ok := pl["preferences"].([]interface{}); ok {
		for _, p := range prefs {
			m, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			spread, _ := m["spread"].(string)
			if spread == "" {
				continue
			}
			key, ok := mapNodeLabelKey(spread)
			if !ok {
				return nil, fmt.Errorf("unsupported placement preference spread %q", spread)
			}
			cfg.spreadKeys = append(cfg.spreadKeys, key)
		}
	}
	if len(cfg.constraints) == 0 && len(cfg.spreadKeys) == 0 {
		return nil, nil
	}
	return cfg, nil
}

// stripPlacement removes deploy.placement so the fork ignores it (orcinus owns it).
func stripPlacement(svc map[string]interface{}) {
	if d, ok := svc["deploy"].(map[string]interface{}); ok {
		delete(d, "placement")
	}
}

// parseConstraint maps one Swarm constraint (`key == value` / `key != value`) to
// a Kubernetes node selector requirement.
func parseConstraint(s string) (nodeConstraint, error) {
	neg := false
	var parts []string
	switch {
	case strings.Contains(s, "!="):
		neg, parts = true, strings.SplitN(s, "!=", 2)
	case strings.Contains(s, "=="):
		parts = strings.SplitN(s, "==", 2)
	default:
		return nodeConstraint{}, fmt.Errorf("invalid placement constraint %q (want 'key == value' or 'key != value')", s)
	}
	key := strings.TrimSpace(parts[0])
	val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)

	// node.role has no value in Kubernetes — it maps to the presence/absence of the
	// control-plane role label.
	if key == "node.role" {
		manager := val == "manager"
		exists := manager != neg // ==manager / !=worker → Exists ; else DoesNotExist
		op := "DoesNotExist"
		if exists {
			op = "Exists"
		}
		return nodeConstraint{Key: "node-role.kubernetes.io/control-plane", Operator: op}, nil
	}
	k8sKey, ok := mapNodeLabelKey(key)
	if !ok {
		return nodeConstraint{}, fmt.Errorf("unsupported placement constraint key %q "+
			"(supported: node.role, node.hostname, node.platform.arch, node.platform.os, node.labels.*)", key)
	}
	op := "In"
	if neg {
		op = "NotIn"
	}
	return nodeConstraint{Key: k8sKey, Operator: op, Values: []string{val}}, nil
}

// mapNodeLabelKey maps a Swarm node attribute to the Kubernetes node label.
func mapNodeLabelKey(k string) (string, bool) {
	switch k {
	case "node.hostname":
		return "kubernetes.io/hostname", true
	case "node.platform.arch":
		return "kubernetes.io/arch", true
	case "node.platform.os", "engine.labels.operatingsystem":
		return "kubernetes.io/os", true
	}
	if strings.HasPrefix(k, "node.labels.") {
		return strings.TrimPrefix(k, "node.labels."), true
	}
	return "", false
}

// stringMapExt reads a mapping extension value into a string map.
func stringMapExt(v interface{}) map[string]string {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, val := range m {
		if s, ok := stringExt(val); ok {
			out[k] = s
		}
	}
	return out
}

// extractBindMounts separates host-path (bind) volumes from a service's
// `volumes:` list. It returns the bind mounts (to become hostPath volumes) and
// the remaining entries (named/anonymous volumes, left for the fork → PVC).
func extractBindMounts(v interface{}, baseDir string) (mounts []bindMount, kept []interface{}) {
	list, ok := v.([]interface{})
	if !ok {
		return nil, nil
	}
	idx := 0
	for _, item := range list {
		switch e := item.(type) {
		case string:
			if src, tgt, ro, isBind := parseShortBind(e); isBind {
				mounts = append(mounts, bindMount{
					Name: fmt.Sprintf("bind-%d", idx), Source: bindMountHostPath(src, baseDir), Target: tgt, ReadOnly: ro,
				})
				idx++
				continue
			}
		case map[string]interface{}:
			if t, _ := e["type"].(string); t == "bind" {
				src, _ := e["source"].(string)
				tgt, _ := e["target"].(string)
				ro, _ := e["read_only"].(bool)
				if src != "" && tgt != "" {
					mounts = append(mounts, bindMount{
						Name: fmt.Sprintf("bind-%d", idx), Source: bindMountHostPath(src, baseDir), Target: tgt, ReadOnly: ro,
					})
					idx++
					continue
				}
			}
		}
		kept = append(kept, item)
	}
	return mounts, kept
}

// parseShortBind parses `SOURCE:TARGET[:MODE]` and reports whether SOURCE is a
// host path (a bind mount) rather than a named volume.
func parseShortBind(s string) (src, tgt string, readOnly, isBind bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "", "", false, false // anonymous volume (target only)
	}
	src = parts[0]
	if !isBindSource(src) {
		return "", "", false, false // named volume
	}
	tgt = parts[1]
	if len(parts) >= 3 && strings.Contains(parts[2], "ro") {
		readOnly = true
	}
	return src, tgt, readOnly, true
}

// isBindSource reports whether a volume source is a host path (absolute,
// relative, or ~) as opposed to a named volume.
func isBindSource(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~")
}

// bindMountHostPath resolves a bind-mount source to an absolute host path
// (Kubernetes hostPath requires an absolute path). Relative paths resolve against
// baseDir (the compose file's directory), matching Docker Compose semantics.
func bindMountHostPath(src, baseDir string) string {
	if strings.HasPrefix(src, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			src = home + strings.TrimPrefix(src, "~")
		}
	}
	if filepath.IsAbs(src) {
		return filepath.Clean(src)
	}
	return absPath(src, baseDir)
}

// absPath resolves rel against baseDir (falling back to the process CWD) → absolute.
func absPath(rel, baseDir string) string {
	if baseDir != "" {
		if abs, err := filepath.Abs(filepath.Join(baseDir, rel)); err == nil {
			return abs
		}
	}
	if abs, err := filepath.Abs(rel); err == nil {
		return abs
	}
	return rel
}

// normalizeLabels accepts compose labels in either map or `["k=v"]` list form and
// returns a map we can extend.
func normalizeLabels(v interface{}) map[string]interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		return t
	case []interface{}:
		m := map[string]interface{}{}
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				continue
			}
			for i := 0; i < len(s); i++ {
				if s[i] == '=' {
					m[s[:i]] = s[i+1:]
					break
				}
			}
		}
		return m
	default:
		return map[string]interface{}{}
	}
}

func stringExt(v interface{}) (string, bool) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return "", false
		}
		return t, true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	case int:
		return fmt.Sprintf("%d", t), true
	case int64:
		return fmt.Sprintf("%d", t), true
	case float64:
		// sigs.k8s.io/yaml decodes numbers as float64; render integers cleanly.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t)), true
		}
		return fmt.Sprintf("%g", t), true
	default:
		return "", false
	}
}

// parseUpdateConfig maps the standard compose `deploy.update_config` onto the
// Kubernetes rolling-update knobs:
//
//	order: start-first → maxSurge=parallelism, maxUnavailable=0
//	order: stop-first  → maxSurge=0, maxUnavailable=parallelism   (compose default)
//	parallelism        → the count for the active knob (default 1)
//	delay              → minReadySeconds
//	monitor            → progressDeadlineSeconds
func parseUpdateConfig(svc map[string]interface{}, sc *strategyCfg) {
	deploy, ok := svc["deploy"].(map[string]interface{})
	if !ok {
		return
	}
	uc, ok := deploy["update_config"].(map[string]interface{})
	if !ok {
		return
	}

	parallelism := 1
	if n, ok := intExt(uc["parallelism"]); ok && n > 0 {
		parallelism = n
	}
	order, _ := stringExt(uc["order"]) // default (empty) == stop-first
	sc.Type = "rolling"
	if order == "start-first" {
		sc.MaxSurge = fmt.Sprintf("%d", parallelism)
		sc.MaxUnavailable = "0"
	} else {
		sc.MaxSurge = "0"
		sc.MaxUnavailable = fmt.Sprintf("%d", parallelism)
	}
	if v, ok := stringExt(uc["delay"]); ok {
		if s, ok := durationSeconds(v); ok {
			sc.MinReadySeconds = s
		}
	}
	if v, ok := stringExt(uc["monitor"]); ok {
		if s, ok := durationSeconds(v); ok {
			sc.ProgressDeadline = s
		}
	}
}

// durationSeconds parses a compose duration ("10s", "1m30s") to whole seconds.
func durationSeconds(s string) (int32, bool) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, false
	}
	return int32(d.Seconds()), true
}

func intExt(v interface{}) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func stringSliceExt(v interface{}) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []interface{}:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

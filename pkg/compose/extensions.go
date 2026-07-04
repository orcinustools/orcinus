package compose

import (
	"fmt"
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
}

// injectKomposeLabels reads x-orcinus-* keys from every service and rewrites the
// compose document so the forked kompose engine sees equivalent native labels.
func injectKomposeLabels(composeBytes []byte) (*preprocessed, error) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose document: %w", err)
	}

	out := &preprocessed{
		secrets:   map[string][]string{},
		ingress:   map[string]ingressCfg{},
		autoscale: map[string]autoscaleCfg{},
		strategy:         map[string]strategyCfg{},
		rollout:          map[string]string{},
		imagePullSecrets: map[string][]string{},
	}

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

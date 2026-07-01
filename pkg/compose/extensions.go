package compose

import (
	"fmt"

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
)

// kompose native label keys we translate onto.
const (
	lblController = "kompose.controller.type"
	lblServiceTy  = "kompose.service.type"
	lblExpose     = "kompose.service.expose"
	lblVolumeSize = "kompose.volume.size"
)

// preprocessed is the result of translating one compose document.
type preprocessed struct {
	// content is the rewritten compose bytes (with kompose.* labels injected).
	content []byte
	// secrets maps a service name to env var names that should be stored in a
	// Secret. Handled after transform since kompose has no direct equivalent.
	secrets map[string][]string
}

// injectKomposeLabels reads x-orcinus-* keys from every service and rewrites the
// compose document so the forked kompose engine sees equivalent native labels.
func injectKomposeLabels(composeBytes []byte) (*preprocessed, error) {
	var doc map[string]interface{}
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		return nil, fmt.Errorf("parse compose document: %w", err)
	}

	out := &preprocessed{secrets: map[string][]string{}}

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

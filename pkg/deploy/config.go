package deploy

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/orcinustools/orcinus/pkg/compose"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// ProjectConfig is the live configuration of one deployed project: every
// orcinus-owned object, in the order `prunableGVRs` lists their types.
type ProjectConfig struct {
	Project   string
	Namespace string
	Items     []unstructured.Unstructured
}

// ProjectConfig reads back everything orcinus owns for a project. It is the
// inverse of deploy: the cluster, not the compose file, is the source of truth,
// so it also shows drift applied with kubectl.
func (a *Applier) ProjectConfig(ctx context.Context, project, namespace string) (*ProjectConfig, error) {
	if project == "" {
		return nil, fmt.Errorf("project name is required")
	}
	if namespace == "" {
		namespace = "default"
	}
	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, compose.LabelProject, project)

	cfg := &ProjectConfig{Project: project, Namespace: namespace}
	for _, gvr := range prunableGVRs {
		list, err := a.dyn.Resource(gvr).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			continue // type may not exist on this cluster
		}
		items := list.Items
		sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
		cfg.Items = append(cfg.Items, items...)
	}
	if len(cfg.Items) == 0 {
		return nil, fmt.Errorf("no orcinus-managed resources for project %q in namespace %q", project, namespace)
	}
	return cfg, nil
}

// --- Kubernetes output ---------------------------------------------------

// RenderK8s returns the project as a multi-document manifest stream, stripped of
// the fields a cluster fills in so the output can be re-applied elsewhere.
//
// Secret data is redacted unless showSecrets is set: this output is meant to be
// read, piped and committed, and printing credentials by default is the wrong
// side to err on. Redacted output is no longer re-appliable as-is.
func (c *ProjectConfig) RenderK8s(showSecrets bool) ([]byte, error) {
	var buf strings.Builder
	for i := range c.Items {
		item := c.Items[i].DeepCopy()
		cleanForExport(item)
		if !showSecrets && item.GetKind() == "Secret" {
			redactSecret(item)
		}
		b, err := yaml.Marshal(item.Object)
		if err != nil {
			return nil, fmt.Errorf("marshal %s/%s: %w", strings.ToLower(item.GetKind()), item.GetName(), err)
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(b)
	}
	return []byte(buf.String()), nil
}

// cleanForExport drops cluster-assigned bookkeeping so the manifest is portable.
func cleanForExport(u *unstructured.Unstructured) {
	cleanForApply(u)
	for _, f := range [][]string{
		{"metadata", "generation"},
		{"metadata", "ownerReferences"},
		{"metadata", "annotations", "kubectl.kubernetes.io/last-applied-configuration"},
		{"metadata", "annotations", "deployment.kubernetes.io/revision"},
		{"spec", "template", "metadata", "creationTimestamp"},
		// Service: cluster-assigned addressing must not be carried to another cluster.
		{"spec", "clusterIP"},
		{"spec", "clusterIPs"},
		// PVC: the bound volume belongs to this cluster only.
		{"spec", "volumeName"},
	} {
		unstructured.RemoveNestedField(u.Object, f...)
	}
	if anns := u.GetAnnotations(); len(anns) == 0 {
		unstructured.RemoveNestedField(u.Object, "metadata", "annotations")
	}

	// A StatefulSet's volumeClaimTemplates are embedded PVCs, and the API server
	// stamps each one with its own creationTimestamp and status.
	if tmpls, ok, _ := unstructured.NestedSlice(u.Object, "spec", "volumeClaimTemplates"); ok {
		for _, t := range tmpls {
			tm, isMap := t.(map[string]any)
			if !isMap {
				continue
			}
			unstructured.RemoveNestedField(tm, "status")
			unstructured.RemoveNestedField(tm, "metadata", "creationTimestamp")
		}
		_ = unstructured.SetNestedSlice(u.Object, tmpls, "spec", "volumeClaimTemplates")
	}
}

func redactSecret(u *unstructured.Unstructured) {
	data, ok, _ := unstructured.NestedMap(u.Object, "data")
	if !ok {
		return
	}
	for k := range data {
		data[k] = "<redacted>"
	}
	_ = unstructured.SetNestedMap(u.Object, data, "data")
	anns := u.GetAnnotations()
	if anns == nil {
		anns = map[string]string{}
	}
	anns["orcinus.io/redacted"] = "values omitted; re-export with --show-secrets"
	u.SetAnnotations(anns)
}

// --- compose output ------------------------------------------------------

// composeFile mirrors the subset of the compose schema orcinus can reconstruct.
// Keys are emitted alphabetically (sigs.k8s.io/yaml), which is stable across
// runs — worth more here than authored key order.
type composeFile struct {
	Services map[string]composeService `json:"services"`
	Volumes  map[string]struct{}       `json:"volumes,omitempty"`
}

type composeService struct {
	Image       string            `json:"image,omitempty"`
	Entrypoint  []string          `json:"entrypoint,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
	Volumes     []string          `json:"volumes,omitempty"`
	Deploy      *composeDeploy    `json:"deploy,omitempty"`

	Controller string `json:"x-orcinus-controller,omitempty"`
	VolumeSize string `json:"x-orcinus-volume-size,omitempty"`
	Expose     string `json:"x-orcinus-expose,omitempty"`
	Host       string `json:"x-orcinus-host,omitempty"`
}

type composeDeploy struct {
	Replicas  *int64            `json:"replicas,omitempty"`
	Resources *composeResources `json:"resources,omitempty"`
}

type composeResources struct {
	Limits map[string]string `json:"limits,omitempty"`
}

// RenderCompose rebuilds an orcinus.yml from the deployed workloads.
//
// This is a best-effort reverse of the compose → Kubernetes conversion, not a
// round-trip guarantee: what a cluster stores is richer than compose can say.
// The header lists whatever had to be dropped so the gap is visible in the
// output itself rather than only in the docs.
func (c *ProjectConfig) RenderCompose() ([]byte, error) {
	file := composeFile{Services: map[string]composeService{}}
	volumes := map[string]struct{}{}
	var notes []string

	for i := range c.Items {
		item := &c.Items[i]
		kind := item.GetKind()
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
			continue
		}
		name := item.GetName()
		svc, svcNotes := c.serviceFromWorkload(item, volumes)
		file.Services[name] = svc
		for _, n := range svcNotes {
			notes = append(notes, name+": "+n)
		}
	}
	if len(file.Services) == 0 {
		return nil, fmt.Errorf("project %q has no Deployment, StatefulSet or DaemonSet to describe as compose", c.Project)
	}
	if len(volumes) > 0 {
		file.Volumes = volumes
	}

	body, err := yaml.Marshal(file)
	if err != nil {
		return nil, err
	}
	header := fmt.Sprintf("# Reconstructed by `orcinus config` from project %q in namespace %q.\n"+
		"# Best-effort: the cluster holds more than compose can express.\n", c.Project, c.Namespace)
	sort.Strings(notes)
	for _, n := range notes {
		header += "# note: " + n + "\n"
	}
	return append([]byte(header), body...), nil
}

// serviceFromWorkload maps one controller to a compose service, recording what
// could not be represented.
func (c *ProjectConfig) serviceFromWorkload(w *unstructured.Unstructured, volumes map[string]struct{}) (composeService, []string) {
	var svc composeService
	var notes []string
	name := w.GetName()

	switch w.GetKind() {
	case "StatefulSet":
		svc.Controller = "statefulset"
	case "DaemonSet":
		svc.Controller = "daemonset"
	}

	containers, _, _ := unstructured.NestedSlice(w.Object, "spec", "template", "spec", "containers")
	if len(containers) == 0 {
		return svc, append(notes, "no containers found")
	}
	if len(containers) > 1 {
		notes = append(notes, fmt.Sprintf("%d containers; compose has one per service, only the first is shown", len(containers)))
	}
	ctr, _ := containers[0].(map[string]any)

	svc.Image, _, _ = unstructured.NestedString(ctr, "image")
	// compose entrypoint → k8s command, compose command → k8s args.
	svc.Entrypoint = stringSlice(ctr, "command")
	svc.Command = stringSlice(ctr, "args")

	if env, ok, _ := unstructured.NestedSlice(ctr, "env"); ok {
		plain := map[string]string{}
		for _, e := range env {
			em, _ := e.(map[string]any)
			key, _, _ := unstructured.NestedString(em, "name")
			if key == "" {
				continue
			}
			if val, ok, _ := unstructured.NestedString(em, "value"); ok {
				plain[key] = val
				continue
			}
			notes = append(notes, fmt.Sprintf("env %s comes from a Secret/ConfigMap reference and is not inlined", key))
		}
		if len(plain) > 0 {
			svc.Environment = plain
		}
	}

	if lim, ok, _ := unstructured.NestedStringMap(ctr, "resources", "limits"); ok && len(lim) > 0 {
		if converted := composeLimits(lim); len(converted) > 0 {
			svc.Deploy = &composeDeploy{Resources: &composeResources{Limits: converted}}
		}
	}
	if w.GetKind() != "DaemonSet" {
		if r, ok, _ := unstructured.NestedInt64(w.Object, "spec", "replicas"); ok {
			if svc.Deploy == nil {
				svc.Deploy = &composeDeploy{}
			}
			svc.Deploy.Replicas = &r
		}
	}

	svc.Ports = c.portsFor(name)
	svc.Volumes, svc.VolumeSize = c.volumesFor(w, ctr, volumes)
	svc.Expose, svc.Host = c.ingressFor(name)
	return svc, notes
}

// portsFor renders the Service in front of a workload as compose port strings.
func (c *ProjectConfig) portsFor(workload string) []string {
	var out []string
	for i := range c.Items {
		item := &c.Items[i]
		if item.GetKind() != "Service" || item.GetName() != workload {
			continue
		}
		ports, _, _ := unstructured.NestedSlice(item.Object, "spec", "ports")
		for _, p := range ports {
			pm, _ := p.(map[string]any)
			port, ok, _ := unstructured.NestedInt64(pm, "port")
			if !ok {
				continue
			}
			target, hasTarget, _ := unstructured.NestedInt64(pm, "targetPort")
			if hasTarget && target != port {
				out = append(out, fmt.Sprintf("%d:%d", port, target))
			} else {
				out = append(out, fmt.Sprintf("%d", port))
			}
		}
	}
	return out
}

// volumesFor maps pod volumes back to compose mounts. PVC-backed volumes become
// named volumes (collected into the top-level `volumes:`), hostPath ones become
// bind mounts. ConfigMap/Secret mounts belong to compose `configs:`/`secrets:`
// and are left out.
func (c *ProjectConfig) volumesFor(w *unstructured.Unstructured, ctr map[string]any, named map[string]struct{}) ([]string, string) {
	mounts := map[string]string{} // volume name → mountPath
	vm, _, _ := unstructured.NestedSlice(ctr, "volumeMounts")
	for _, m := range vm {
		mm, _ := m.(map[string]any)
		n, _, _ := unstructured.NestedString(mm, "name")
		p, _, _ := unstructured.NestedString(mm, "mountPath")
		if n != "" && p != "" {
			mounts[n] = p
		}
	}

	var out []string
	size := ""

	// A StatefulSet's claims live in volumeClaimTemplates, not in the pod spec.
	tmpls, _, _ := unstructured.NestedSlice(w.Object, "spec", "volumeClaimTemplates")
	for _, t := range tmpls {
		tm, _ := t.(map[string]any)
		n, _, _ := unstructured.NestedString(tm, "metadata", "name")
		if n == "" || mounts[n] == "" {
			continue
		}
		out = append(out, n+":"+mounts[n])
		named[n] = struct{}{}
		if s, ok, _ := unstructured.NestedString(tm, "spec", "resources", "requests", "storage"); ok {
			size = s
		}
	}

	vols, _, _ := unstructured.NestedSlice(w.Object, "spec", "template", "spec", "volumes")
	for _, v := range vols {
		vmap, _ := v.(map[string]any)
		n, _, _ := unstructured.NestedString(vmap, "name")
		path := mounts[n]
		if n == "" || path == "" {
			continue
		}
		switch {
		case hasField(vmap, "persistentVolumeClaim"):
			claim, _, _ := unstructured.NestedString(vmap, "persistentVolumeClaim", "claimName")
			if claim == "" {
				claim = n
			}
			if _, dup := named[claim]; dup {
				continue // already emitted from a volumeClaimTemplate
			}
			out = append(out, claim+":"+path)
			named[claim] = struct{}{}
			if s := c.claimSize(claim); s != "" {
				size = s
			}
		case hasField(vmap, "hostPath"):
			host, _, _ := unstructured.NestedString(vmap, "hostPath", "path")
			if host != "" {
				out = append(out, host+":"+path)
			}
		}
	}
	sort.Strings(out)
	return out, size
}

// composeLimits translates Kubernetes resource limits into compose's spelling:
// `cpu` is `cpus` there, and it wants a fractional core count rather than
// Kubernetes' milli-CPU suffix. Keys compose has no equivalent for are dropped.
func composeLimits(k8s map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range k8s {
		switch k {
		case "cpu":
			out["cpus"] = milliCPUToCores(v)
		case "memory":
			out["memory"] = v
		}
	}
	return out
}

// milliCPUToCores turns "500m" into "0.5"; anything else is already a core
// count and passes through.
func milliCPUToCores(v string) string {
	milli, ok := strings.CutSuffix(v, "m")
	if !ok {
		return v
	}
	n, err := strconv.ParseFloat(milli, 64)
	if err != nil {
		return v
	}
	return strconv.FormatFloat(n/1000, 'f', -1, 64)
}

func (c *ProjectConfig) claimSize(name string) string {
	for i := range c.Items {
		item := &c.Items[i]
		if item.GetKind() == "PersistentVolumeClaim" && item.GetName() == name {
			s, _, _ := unstructured.NestedString(item.Object, "spec", "resources", "requests", "storage")
			return s
		}
	}
	return ""
}

// ingressFor returns the x-orcinus-expose/host pair for a workload, by finding
// the Ingress that routes to its Service.
func (c *ProjectConfig) ingressFor(workload string) (expose, host string) {
	for i := range c.Items {
		item := &c.Items[i]
		if item.GetKind() != "Ingress" {
			continue
		}
		rules, _, _ := unstructured.NestedSlice(item.Object, "spec", "rules")
		for _, r := range rules {
			rm, _ := r.(map[string]any)
			paths, _, _ := unstructured.NestedSlice(rm, "http", "paths")
			for _, p := range paths {
				pm, _ := p.(map[string]any)
				svc, _, _ := unstructured.NestedString(pm, "backend", "service", "name")
				if svc == workload {
					h, _, _ := unstructured.NestedString(rm, "host")
					return "ingress", h
				}
			}
		}
	}
	return "", ""
}

// --- summary output ------------------------------------------------------

// WriteSummary prints a human-readable overview: one row per workload, then the
// supporting objects. It is what `orcinus config` shows without a format.
func (c *ProjectConfig) WriteSummary(out io.Writer) error {
	fmt.Fprintf(out, "Project:    %s\nNamespace:  %s\nResources:  %d\n\n", c.Project, c.Namespace, len(c.Items))

	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tKIND\tIMAGE\tREPLICAS\tPORTS\tVOLUMES")
	workloads := 0
	for i := range c.Items {
		item := &c.Items[i]
		kind := item.GetKind()
		if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
			continue
		}
		workloads++
		svc, _ := c.serviceFromWorkload(item, map[string]struct{}{})
		replicas := "-"
		if svc.Deploy != nil && svc.Deploy.Replicas != nil {
			ready, _, _ := unstructured.NestedInt64(item.Object, "status", "readyReplicas")
			replicas = fmt.Sprintf("%d/%d", ready, *svc.Deploy.Replicas)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", item.GetName(), kind, svc.Image, replicas,
			orDash(strings.Join(svc.Ports, ",")), orDash(strings.Join(svc.Volumes, ",")))
	}
	if workloads == 0 {
		fmt.Fprintln(tw, "(none)\t\t\t\t\t")
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	others := map[string][]string{}
	for i := range c.Items {
		item := &c.Items[i]
		switch item.GetKind() {
		case "Deployment", "StatefulSet", "DaemonSet":
		default:
			others[item.GetKind()] = append(others[item.GetKind()], item.GetName())
		}
	}
	if len(others) > 0 {
		fmt.Fprintln(out)
		kinds := make([]string, 0, len(others))
		for k := range others {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			fmt.Fprintf(out, "%-24s %s\n", k+":", strings.Join(others[k], ", "))
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func hasField(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

func stringSlice(m map[string]any, key string) []string {
	raw, ok, _ := unstructured.NestedStringSlice(m, key)
	if !ok {
		return nil
	}
	return raw
}

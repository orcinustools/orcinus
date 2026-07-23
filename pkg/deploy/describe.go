package deploy

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/orcinustools/orcinus/pkg/compose"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

// DescribePod renders a kubectl-style detailed view of a single pod, including
// its events, and writes it to out (backs `orcinus describe pod`).
func (a *Applier) DescribePod(ctx context.Context, name, namespace string, out io.Writer) error {
	if name == "" {
		return fmt.Errorf("pod name is required")
	}
	if namespace == "" {
		namespace = "default"
	}
	pod, err := a.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	// Events are best-effort: a missing events endpoint should not fail describe.
	events, _ := a.objectEvents(ctx, pod.Namespace, string(pod.UID), pod.Name)
	return renderPod(out, pod, events)
}

// objectEvents fetches the events referencing an object (by uid+name, scoped to
// namespace when non-empty), oldest first. An empty namespace searches all
// namespaces — used for cluster-scoped objects like nodes.
func (a *Applier) objectEvents(ctx context.Context, namespace, uid, name string) ([]corev1.Event, error) {
	terms := []fields.Selector{
		fields.OneTermEqualSelector("involvedObject.uid", uid),
		fields.OneTermEqualSelector("involvedObject.name", name),
	}
	if namespace != "" {
		terms = append(terms, fields.OneTermEqualSelector("involvedObject.namespace", namespace))
	}
	sel := fields.AndSelectors(terms...).String()
	list, err := a.clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, err
	}
	evs := list.Items
	sort.Slice(evs, func(i, j int) bool {
		return eventTime(&evs[i]).Before(eventTime(&evs[j]))
	})
	return evs, nil
}

func renderPod(out io.Writer, pod *corev1.Pod, events []corev1.Event) error {
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }

	p("Name:\t%s\n", pod.Name)
	p("Namespace:\t%s\n", pod.Namespace)
	if pod.Spec.PriorityClassName != "" {
		p("Priority Class Name:\t%s\n", pod.Spec.PriorityClassName)
	}
	if pod.Spec.NodeName != "" {
		p("Node:\t%s\n", pod.Spec.NodeName)
	} else {
		p("Node:\t<none>\n")
	}
	if pod.Status.StartTime != nil {
		p("Start Time:\t%s\n", pod.Status.StartTime.Time.Format(time.RFC1123Z))
	}
	p("Labels:\t%s\n", describeMap(pod.Labels))
	p("Annotations:\t%s\n", describeMap(pod.Annotations))
	p("Status:\t%s\n", podStatus(pod))
	if pod.Status.Reason != "" {
		p("Reason:\t%s\n", pod.Status.Reason)
	}
	if pod.Status.Message != "" {
		p("Message:\t%s\n", pod.Status.Message)
	}
	p("IP:\t%s\n", orNone(pod.Status.PodIP))
	if len(pod.Status.PodIPs) > 0 {
		ips := make([]string, 0, len(pod.Status.PodIPs))
		for _, ip := range pod.Status.PodIPs {
			ips = append(ips, ip.IP)
		}
		p("IPs:\t%s\n", strings.Join(ips, ", "))
	}
	if ctrl := metav1.GetControllerOf(pod); ctrl != nil {
		p("Controlled By:\t%s/%s\n", ctrl.Kind, ctrl.Name)
	}

	p("Containers:\n")
	for _, c := range pod.Spec.Containers {
		describeContainer(w, c, containerStatus(pod.Status.ContainerStatuses, c.Name))
	}

	p("Conditions:\n")
	p("  Type\tStatus\n")
	for _, cond := range pod.Status.Conditions {
		p("  %s\t%s\n", cond.Type, cond.Status)
	}

	describeVolumes(w, pod.Spec.Volumes)

	if pod.Status.QOSClass != "" {
		p("QoS Class:\t%s\n", pod.Status.QOSClass)
	}
	p("Node-Selectors:\t%s\n", describeMap(pod.Spec.NodeSelector))
	p("Tolerations:\t%s\n", describeTolerations(pod.Spec.Tolerations))

	describeEvents(w, events)
	return w.Flush()
}

func describeContainer(w io.Writer, c corev1.Container, cs *corev1.ContainerStatus) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	p("  %s:\n", c.Name)
	if cs != nil {
		p("    Container ID:\t%s\n", orNone(cs.ContainerID))
	}
	p("    Image:\t%s\n", c.Image)
	if cs != nil {
		p("    Image ID:\t%s\n", orNone(cs.ImageID))
	}
	if len(c.Ports) > 0 {
		ports := make([]string, 0, len(c.Ports))
		for _, port := range c.Ports {
			ports = append(ports, fmt.Sprintf("%d/%s", port.ContainerPort, port.Protocol))
		}
		p("    Ports:\t%s\n", strings.Join(ports, ", "))
	}
	if cmd := strings.Join(c.Command, " "); cmd != "" {
		p("    Command:\t%s\n", cmd)
	}
	if arg := strings.Join(c.Args, " "); arg != "" {
		p("    Args:\t%s\n", arg)
	}
	if cs != nil {
		p("    State:\t%s\n", containerState(cs.State))
		p("    Ready:\t%t\n", cs.Ready)
		p("    Restart Count:\t%d\n", cs.RestartCount)
	}
	describeResources(w, c.Resources)
	describeEnv(w, c.Env)
	describeMounts(w, c.VolumeMounts)
}

func describeResources(w io.Writer, r corev1.ResourceRequirements) {
	if len(r.Limits) == 0 && len(r.Requests) == 0 {
		return
	}
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	if len(r.Limits) > 0 {
		p("    Limits:\n")
		for _, k := range sortedResourceNames(r.Limits) {
			q := r.Limits[k]
			p("      %s:\t%s\n", k, q.String())
		}
	}
	if len(r.Requests) > 0 {
		p("    Requests:\n")
		for _, k := range sortedResourceNames(r.Requests) {
			q := r.Requests[k]
			p("      %s:\t%s\n", k, q.String())
		}
	}
}

func describeEnv(w io.Writer, env []corev1.EnvVar) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	if len(env) == 0 {
		p("    Environment:\t<none>\n")
		return
	}
	p("    Environment:\n")
	for _, e := range env {
		switch {
		case e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil:
			p("      %s:\t<set to the key '%s' in secret '%s'>\n", e.Name, e.ValueFrom.SecretKeyRef.Key, e.ValueFrom.SecretKeyRef.Name)
		case e.ValueFrom != nil && e.ValueFrom.ConfigMapKeyRef != nil:
			p("      %s:\t<set to the key '%s' of config map '%s'>\n", e.Name, e.ValueFrom.ConfigMapKeyRef.Key, e.ValueFrom.ConfigMapKeyRef.Name)
		case e.ValueFrom != nil && e.ValueFrom.FieldRef != nil:
			p("      %s:\t(%s)\n", e.Name, e.ValueFrom.FieldRef.FieldPath)
		default:
			p("      %s:\t%s\n", e.Name, e.Value)
		}
	}
}

func describeMounts(w io.Writer, mounts []corev1.VolumeMount) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	if len(mounts) == 0 {
		p("    Mounts:\t<none>\n")
		return
	}
	p("    Mounts:\n")
	for _, m := range mounts {
		ro := "rw"
		if m.ReadOnly {
			ro = "ro"
		}
		p("      %s from %s (%s)\n", m.MountPath, m.Name, ro)
	}
}

func describeVolumes(w io.Writer, volumes []corev1.Volume) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	if len(volumes) == 0 {
		return
	}
	p("Volumes:\n")
	for _, v := range volumes {
		p("  %s:\n", v.Name)
		switch {
		case v.PersistentVolumeClaim != nil:
			p("    Type:\tPersistentVolumeClaim\n")
			p("    ClaimName:\t%s\n", v.PersistentVolumeClaim.ClaimName)
		case v.ConfigMap != nil:
			p("    Type:\tConfigMap\n")
			p("    Name:\t%s\n", v.ConfigMap.Name)
		case v.Secret != nil:
			p("    Type:\tSecret\n")
			p("    SecretName:\t%s\n", v.Secret.SecretName)
		case v.EmptyDir != nil:
			p("    Type:\tEmptyDir\n")
		case v.HostPath != nil:
			p("    Type:\tHostPath\n")
			p("    Path:\t%s\n", v.HostPath.Path)
		default:
			p("    Type:\t<other>\n")
		}
	}
}

func describeEvents(w io.Writer, events []corev1.Event) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	if len(events) == 0 {
		p("Events:\t<none>\n")
		return
	}
	p("Events:\n")
	p("  Type\tReason\tAge\tFrom\tMessage\n")
	p("  ----\t------\t----\t----\t-------\n")
	for i := range events {
		e := &events[i]
		p("  %s\t%s\t%s\t%s\t%s\n",
			orNone(e.Type), e.Reason, eventAge(e), e.Source.Component, strings.TrimSpace(e.Message))
	}
}

// DescribeService renders a detailed view of the workload (Deployment or
// StatefulSet) backing a compose service, plus its events (backs
// `orcinus describe service`). project, when set, further scopes the lookup.
func (a *Applier) DescribeService(ctx context.Context, service, project, namespace string, out io.Writer) error {
	if service == "" {
		return fmt.Errorf("service name is required")
	}
	if namespace == "" {
		namespace = "default"
	}
	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, serviceLabel, service)
	if project != "" {
		selector += fmt.Sprintf(",%s=%s", compose.LabelProject, project)
	}
	opts := metav1.ListOptions{LabelSelector: selector}

	deps, err := a.clientset.AppsV1().Deployments(namespace).List(ctx, opts)
	if err != nil {
		return err
	}
	if len(deps.Items) > 0 {
		for i := range deps.Items {
			d := &deps.Items[i]
			events, _ := a.objectEvents(ctx, d.Namespace, string(d.UID), d.Name)
			if err := renderDeployment(out, d, events); err != nil {
				return err
			}
		}
		return nil
	}

	sts, err := a.clientset.AppsV1().StatefulSets(namespace).List(ctx, opts)
	if err != nil {
		return err
	}
	if len(sts.Items) > 0 {
		for i := range sts.Items {
			s := &sts.Items[i]
			events, _ := a.objectEvents(ctx, s.Namespace, string(s.UID), s.Name)
			if err := renderStatefulSet(out, s, events); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("no Deployment or StatefulSet found for service %q in namespace %q", service, namespace)
}

// DescribeProject renders an aggregate summary of a whole orcinus project — its
// workloads and pods (backs `orcinus describe project`). An empty namespace
// searches across all namespaces.
func (a *Applier) DescribeProject(ctx context.Context, project, namespace string, out io.Writer) error {
	if project == "" {
		return fmt.Errorf("project name is required")
	}
	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, compose.LabelProject, project)
	opts := metav1.ListOptions{LabelSelector: selector}

	deps, err := a.clientset.AppsV1().Deployments(namespace).List(ctx, opts)
	if err != nil {
		return err
	}
	sts, err := a.clientset.AppsV1().StatefulSets(namespace).List(ctx, opts)
	if err != nil {
		return err
	}
	pods, err := a.listPods(ctx, namespace, selector)
	if err != nil {
		return err
	}
	if len(deps.Items) == 0 && len(sts.Items) == 0 && len(pods) == 0 {
		return fmt.Errorf("no resources found for project %q", project)
	}

	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	p("Name:\t%s\n", project)
	if namespace == "" {
		p("Namespace:\t(all)\n")
	} else {
		p("Namespace:\t%s\n", namespace)
	}
	p("Managed By:\t%s\n", compose.ManagedByValue)

	p("Workloads:\n")
	if len(deps.Items) == 0 && len(sts.Items) == 0 {
		p("  <none>\n")
	} else {
		p("  KIND\tSERVICE\tNAME\tREADY\n")
		for i := range deps.Items {
			d := &deps.Items[i]
			p("  Deployment\t%s\t%s\t%d/%d\n", d.Labels[serviceLabel], d.Name, d.Status.ReadyReplicas, replicasOf(d.Spec.Replicas))
		}
		for i := range sts.Items {
			s := &sts.Items[i]
			p("  StatefulSet\t%s\t%s\t%d/%d\n", s.Labels[serviceLabel], s.Name, s.Status.ReadyReplicas, replicasOf(s.Spec.Replicas))
		}
	}

	p("Pods:\n")
	if len(pods) == 0 {
		p("  <none>\n")
	} else {
		p("  SERVICE\tPOD\tREADY\tSTATUS\tRESTARTS\tNODE\n")
		for _, pod := range pods {
			p("  %s\t%s\t%s\t%s\t%d\t%s\n", pod.Service, pod.Name, pod.Ready, pod.Status, pod.Restarts, orNone(pod.Node))
		}
	}
	return w.Flush()
}

// DescribeNode renders a kubectl-style detailed view of a cluster node, plus its
// events (backs `orcinus describe node`).
func (a *Applier) DescribeNode(ctx context.Context, name string, out io.Writer) error {
	if name == "" {
		return fmt.Errorf("node name is required")
	}
	node, err := a.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	events, _ := a.objectEvents(ctx, "", string(node.UID), node.Name)
	return renderNode(out, node, events)
}

func renderDeployment(out io.Writer, d *appsv1.Deployment, events []corev1.Event) error {
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	p("Name:\t%s\n", d.Name)
	p("Namespace:\t%s\n", d.Namespace)
	p("CreationTimestamp:\t%s\n", d.CreationTimestamp.Time.Format(time.RFC1123Z))
	p("Labels:\t%s\n", describeMap(d.Labels))
	p("Annotations:\t%s\n", describeMap(d.Annotations))
	p("Selector:\t%s\n", metav1.FormatLabelSelector(d.Spec.Selector))
	p("Replicas:\t%d desired | %d updated | %d total | %d available | %d unavailable\n",
		replicasOf(d.Spec.Replicas), d.Status.UpdatedReplicas, d.Status.Replicas,
		d.Status.AvailableReplicas, d.Status.UnavailableReplicas)
	p("StrategyType:\t%s\n", d.Spec.Strategy.Type)
	if d.Spec.MinReadySeconds > 0 {
		p("MinReadySeconds:\t%d\n", d.Spec.MinReadySeconds)
	}
	if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
		mu, ms := "<nil>", "<nil>"
		if ru.MaxUnavailable != nil {
			mu = ru.MaxUnavailable.String()
		}
		if ru.MaxSurge != nil {
			ms = ru.MaxSurge.String()
		}
		p("RollingUpdateStrategy:\t%s max unavailable, %s max surge\n", mu, ms)
	}
	renderPodTemplate(w, d.Spec.Template)
	renderWorkloadConditions(w, deploymentConditions(d.Status.Conditions))
	describeEvents(w, events)
	return w.Flush()
}

func renderStatefulSet(out io.Writer, s *appsv1.StatefulSet, events []corev1.Event) error {
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	p("Name:\t%s\n", s.Name)
	p("Namespace:\t%s\n", s.Namespace)
	p("CreationTimestamp:\t%s\n", s.CreationTimestamp.Time.Format(time.RFC1123Z))
	p("Labels:\t%s\n", describeMap(s.Labels))
	p("Annotations:\t%s\n", describeMap(s.Annotations))
	p("Selector:\t%s\n", metav1.FormatLabelSelector(s.Spec.Selector))
	p("Service Name:\t%s\n", s.Spec.ServiceName)
	p("Replicas:\t%d desired | %d ready | %d current | %d updated\n",
		replicasOf(s.Spec.Replicas), s.Status.ReadyReplicas, s.Status.CurrentReplicas, s.Status.UpdatedReplicas)
	p("Update Strategy:\t%s\n", s.Spec.UpdateStrategy.Type)
	renderPodTemplate(w, s.Spec.Template)
	describeEvents(w, events)
	return w.Flush()
}

func renderPodTemplate(w io.Writer, tpl corev1.PodTemplateSpec) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	p("Pod Template:\n")
	p("  Labels:\t%s\n", describeMap(tpl.Labels))
	p("  Containers:\n")
	for _, c := range tpl.Spec.Containers {
		describeContainer(w, c, nil)
	}
	if len(tpl.Spec.Volumes) > 0 {
		describeVolumes(w, tpl.Spec.Volumes)
	}
}

// workloadCondition is a kind-agnostic view of a workload status condition.
type workloadCondition struct {
	Type, Status, Reason string
}

func deploymentConditions(conds []appsv1.DeploymentCondition) []workloadCondition {
	out := make([]workloadCondition, 0, len(conds))
	for _, c := range conds {
		out = append(out, workloadCondition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason})
	}
	return out
}

func renderWorkloadConditions(w io.Writer, conds []workloadCondition) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	if len(conds) == 0 {
		return
	}
	p("Conditions:\n")
	p("  Type\tStatus\tReason\n")
	for _, c := range conds {
		p("  %s\t%s\t%s\n", c.Type, c.Status, orNone(c.Reason))
	}
}

func renderNode(out io.Writer, node *corev1.Node, events []corev1.Event) error {
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	p("Name:\t%s\n", node.Name)
	p("Roles:\t%s\n", nodeRoles(node))
	p("Labels:\t%s\n", describeMap(node.Labels))
	p("Annotations:\t%s\n", describeMap(node.Annotations))
	p("CreationTimestamp:\t%s\n", node.CreationTimestamp.Time.Format(time.RFC1123Z))
	p("Taints:\t%s\n", nodeTaints(node.Spec.Taints))
	p("Unschedulable:\t%t\n", node.Spec.Unschedulable)

	p("Conditions:\n")
	p("  Type\tStatus\tReason\tMessage\n")
	for _, c := range node.Status.Conditions {
		p("  %s\t%s\t%s\t%s\n", c.Type, c.Status, orNone(c.Reason), strings.TrimSpace(c.Message))
	}

	p("Addresses:\n")
	for _, addr := range node.Status.Addresses {
		p("  %s:\t%s\n", addr.Type, addr.Address)
	}

	p("Capacity:\n")
	for _, k := range sortedResourceNames(node.Status.Capacity) {
		q := node.Status.Capacity[k]
		p("  %s:\t%s\n", k, q.String())
	}
	p("Allocatable:\n")
	for _, k := range sortedResourceNames(node.Status.Allocatable) {
		q := node.Status.Allocatable[k]
		p("  %s:\t%s\n", k, q.String())
	}

	ni := node.Status.NodeInfo
	p("System Info:\n")
	p("  Architecture:\t%s\n", ni.Architecture)
	p("  Operating System:\t%s\n", ni.OperatingSystem)
	p("  OS Image:\t%s\n", ni.OSImage)
	p("  Kernel Version:\t%s\n", ni.KernelVersion)
	p("  Container Runtime:\t%s\n", ni.ContainerRuntimeVersion)
	p("  Kubelet Version:\t%s\n", ni.KubeletVersion)

	describeEvents(w, events)
	return w.Flush()
}

// helpers ---------------------------------------------------------------------

func replicasOf(r *int32) int32 {
	if r == nil {
		return 0
	}
	return *r
}

func nodeRoles(node *corev1.Node) string {
	var roles []string
	for k := range node.Labels {
		if r := strings.TrimPrefix(k, "node-role.kubernetes.io/"); r != k && r != "" {
			roles = append(roles, r)
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		return "<none>"
	}
	return strings.Join(roles, ",")
}

func nodeTaints(taints []corev1.Taint) string {
	if len(taints) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(taints))
	for _, t := range taints {
		s := t.Key
		if t.Value != "" {
			s += "=" + t.Value
		}
		parts = append(parts, s+":"+string(t.Effect))
	}
	return strings.Join(parts, "\n\t")
}

func containerStatus(statuses []corev1.ContainerStatus, name string) *corev1.ContainerStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}
	return nil
}

func containerState(s corev1.ContainerState) string {
	switch {
	case s.Running != nil:
		return fmt.Sprintf("Running (started %s)", s.Running.StartedAt.Time.Format(time.RFC1123Z))
	case s.Waiting != nil:
		if s.Waiting.Reason != "" {
			return "Waiting (" + s.Waiting.Reason + ")"
		}
		return "Waiting"
	case s.Terminated != nil:
		return fmt.Sprintf("Terminated (%s, exit %d)", s.Terminated.Reason, s.Terminated.ExitCode)
	default:
		return "Unknown"
	}
}

func describeMap(m map[string]string) string {
	if len(m) == 0 {
		return "<none>"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, "\n\t")
}

func describeTolerations(tols []corev1.Toleration) string {
	if len(tols) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(tols))
	for _, t := range tols {
		s := t.Key
		if t.Value != "" {
			s += "=" + t.Value
		}
		if t.Effect != "" {
			s += ":" + string(t.Effect)
		}
		if s == "" {
			s = string(t.Operator)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\t")
}

func sortedResourceNames(rl corev1.ResourceList) []corev1.ResourceName {
	keys := make([]corev1.ResourceName, 0, len(rl))
	for k := range rl {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func eventTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

func eventAge(e *corev1.Event) string {
	t := eventTime(e)
	if t.IsZero() {
		return "<unknown>"
	}
	return translateTimestampSince(t)
}

// translateTimestampSince renders a coarse "5m", "3h", "2d" style age, matching
// kubectl's human-readable duration column closely enough for a summary.
func translateTimestampSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

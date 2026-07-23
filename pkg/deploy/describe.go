package deploy

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

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
	events, err := a.podEvents(ctx, pod)
	if err != nil {
		// Events are best-effort: a missing events endpoint should not fail describe.
		events = nil
	}
	return renderPod(out, pod, events)
}

// podEvents fetches the events referencing the given pod, oldest first.
func (a *Applier) podEvents(ctx context.Context, pod *corev1.Pod) ([]corev1.Event, error) {
	sel := fields.AndSelectors(
		fields.OneTermEqualSelector("involvedObject.uid", string(pod.UID)),
		fields.OneTermEqualSelector("involvedObject.name", pod.Name),
		fields.OneTermEqualSelector("involvedObject.namespace", pod.Namespace),
	).String()
	list, err := a.clientset.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{FieldSelector: sel})
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

// helpers ---------------------------------------------------------------------

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

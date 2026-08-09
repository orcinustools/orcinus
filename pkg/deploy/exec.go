package deploy

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/orcinustools/orcinus/pkg/compose"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// defaultContainerAnnotation is the standard hint for which container a
// multi-container pod wants tools to talk to. kubectl honors it; so do we.
const defaultContainerAnnotation = "kubectl.kubernetes.io/default-container"

// ExecOptions describes one `orcinus exec` session.
type ExecOptions struct {
	// Target names a compose service or, when no service matches, a pod. Pod
	// pins an exact pod and skips service resolution entirely; exactly one of
	// the two is required.
	Target string
	Pod    string
	// Project further scopes service resolution, mirroring `orcinus logs`.
	Project   string
	Namespace string
	// Container picks one container of a multi-container pod. Empty means the
	// pod's default-container annotation, else its first container.
	Container string

	Command []string
	TTY     bool

	// Stdin is attached only when non-nil; Stdout/Stderr likewise. With TTY the
	// remote merges stderr into stdout, so Stderr is ignored.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// TerminalSizeQueue propagates window resizes; only meaningful with TTY.
	TerminalSizeQueue remotecommand.TerminalSizeQueue
	// Notify receives human-facing notes about choices made on the caller's
	// behalf ("picked pod X", "defaulted to container Y"). Optional.
	Notify func(string)
}

// Exec runs a command inside a pod of an orcinus-managed service and wires the
// caller's streams to it (backs `orcinus exec`). A command that exits non-zero
// comes back as a k8s.io/client-go/util/exec.CodeExitError carrying the remote
// status, so callers can propagate it rather than flatten it to 1.
func (a *Applier) Exec(ctx context.Context, opts ExecOptions) error {
	if len(opts.Command) == 0 {
		return fmt.Errorf("a command to run is required")
	}
	if opts.TTY && opts.Stdin == nil {
		return fmt.Errorf("a TTY needs stdin attached")
	}
	if a.cfg == nil {
		return fmt.Errorf("exec needs a cluster connection")
	}

	pod, err := a.resolveExecPod(ctx, opts)
	if err != nil {
		return err
	}
	container, err := pickExecContainer(pod, opts.Container, opts.Notify)
	if err != nil {
		return err
	}

	req := a.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   opts.Command,
			Stdin:     opts.Stdin != nil,
			Stdout:    opts.Stdout != nil,
			// The API server rejects a separate stderr stream on a TTY: a
			// terminal has one output, and the remote already folds stderr in.
			Stderr: opts.Stderr != nil && !opts.TTY,
			TTY:    opts.TTY,
		}, scheme.ParameterCodec)

	exec, err := newExecutor(a.cfg, req.URL())
	if err != nil {
		return err
	}
	streams := remotecommand.StreamOptions{
		Stdin:             opts.Stdin,
		Stdout:            opts.Stdout,
		Tty:               opts.TTY,
		TerminalSizeQueue: opts.TerminalSizeQueue,
	}
	if !opts.TTY {
		streams.Stderr = opts.Stderr
	}
	return exec.StreamWithContext(ctx, streams)
}

// newExecutor prefers the websocket transport (RemoteCommand v5) and falls back
// to SPDY when the upgrade is refused, which is what an older API server or an
// intermediate proxy does.
func newExecutor(cfg *rest.Config, u *url.URL) (remotecommand.Executor, error) {
	ws, err := remotecommand.NewWebSocketExecutor(cfg, "GET", u.String())
	if err != nil {
		return nil, err
	}
	spdy, err := remotecommand.NewSPDYExecutor(cfg, "POST", u)
	if err != nil {
		return nil, err
	}
	return remotecommand.NewFallbackExecutor(ws, spdy, httpstream.IsUpgradeFailure)
}

// resolveExecPod turns the caller's target into a concrete pod: an exact pod
// name when Pod is set, otherwise a running pod of the named service — falling
// back to an exact pod name so a name pasted out of `orcinus ps` also works.
func (a *Applier) resolveExecPod(ctx context.Context, opts ExecOptions) (*corev1.Pod, error) {
	namespace := opts.Namespace
	if namespace == "" {
		namespace = "default"
	}
	if opts.Pod != "" {
		pod, err := a.clientset.CoreV1().Pods(namespace).Get(ctx, opts.Pod, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("no pod %q in namespace %q", opts.Pod, namespace)
			}
			return nil, err
		}
		return pod, nil
	}
	if opts.Target == "" {
		return nil, fmt.Errorf("a service or pod name is required")
	}

	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, serviceLabel, opts.Target)
	if opts.Project != "" {
		selector += fmt.Sprintf(",%s=%s", compose.LabelProject, opts.Project)
	}
	list, err := a.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		// Not a service we manage — the target may be a pod name. A --project
		// scope still applies, so a pod from another app is not a match.
		pod, err := a.clientset.CoreV1().Pods(namespace).Get(ctx, opts.Target, metav1.GetOptions{})
		if err == nil && (opts.Project == "" || pod.Labels[compose.LabelProject] == opts.Project) {
			return pod, nil
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		scope := fmt.Sprintf("namespace %q", namespace)
		if opts.Project != "" {
			scope = fmt.Sprintf("project %q (namespace %q)", opts.Project, namespace)
		}
		return nil, fmt.Errorf("no service or pod %q in %s", opts.Target, scope)
	}

	pods := list.Items
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	var running []*corev1.Pod
	for i := range pods {
		if pods[i].DeletionTimestamp == nil && pods[i].Status.Phase == corev1.PodRunning {
			running = append(running, &pods[i])
		}
	}
	if len(running) == 0 {
		var states []string
		for i := range pods {
			states = append(states, fmt.Sprintf("%s: %s", pods[i].Name, podStatus(&pods[i])))
		}
		return nil, fmt.Errorf("service %q has no running pod (%s)", opts.Target, strings.Join(states, ", "))
	}
	if len(running) > 1 && opts.Notify != nil {
		opts.Notify(fmt.Sprintf("service %q has %d running pods; using %s (pin one with --pod)",
			opts.Target, len(running), running[0].Name))
	}
	return running[0], nil
}

// pickExecContainer resolves which container of pod to enter.
func pickExecContainer(pod *corev1.Pod, want string, notify func(string)) (string, error) {
	names := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("pod %q has no containers", pod.Name)
	}
	if want != "" {
		for _, n := range names {
			if n == want {
				return n, nil
			}
		}
		return "", fmt.Errorf("no container %q in pod %q (has: %s)", want, pod.Name, strings.Join(names, ", "))
	}
	if hinted := pod.Annotations[defaultContainerAnnotation]; hinted != "" {
		for _, n := range names {
			if n == hinted {
				return n, nil
			}
		}
	}
	if len(names) > 1 && notify != nil {
		notify(fmt.Sprintf("pod %q has %d containers; using %s (pick one with -c)",
			pod.Name, len(names), names[0]))
	}
	return names[0], nil
}

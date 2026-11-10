// Package monitor reads what runs inside containers — commands executed and
// files changed — from the Tetragon plugin, and turns its shell blocking on and
// off. Tetragon watches the kernel (eBPF) on every node; its agents write events
// as JSON lines, which this package reads back from the agents' export-stdout
// containers.
package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/orcinustools/orcinus/pkg/compose"
	"github.com/orcinustools/orcinus/pkg/deploy"
	"github.com/orcinustools/orcinus/pkg/plugin"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
)

// Event is one thing that happened in a container.
type Event struct {
	Time      time.Time `json:"time"`
	Node      string    `json:"node,omitempty"`
	Kind      string    `json:"kind"` // exec | session | blocked | write | truncate | delete | chmod
	Namespace string    `json:"namespace"`
	Pod       string    `json:"pod"`
	Container string    `json:"container,omitempty"`
	Service   string    `json:"service,omitempty"`
	Image     string    `json:"image,omitempty"`
	Binary    string    `json:"binary,omitempty"` // the process that did it
	Args      string    `json:"args,omitempty"`
	Path      string    `json:"path,omitempty"` // the file (file events) or program (blocked)
	Mode      string    `json:"mode,omitempty"` // chmod: the new mode, octal
	UID       int       `json:"uid"`
	Parent    string    `json:"parent,omitempty"`
}

// FileEvent says whether e is a file change (as opposed to a process event).
func (e Event) FileEvent() bool {
	switch e.Kind {
	case "write", "truncate", "delete", "chmod":
		return true
	}
	return false
}

type tgProcess struct {
	Binary    string `json:"binary"`
	Arguments string `json:"arguments"`
	UID       int    `json:"uid"`
	Pod       *struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Container struct {
			Name  string `json:"name"`
			Image struct {
				Name string `json:"name"`
			} `json:"image"`
		} `json:"container"`
		Labels   map[string]string `json:"pod_labels"`
		Workload string            `json:"workload"`
	} `json:"pod"`
}

type tgArg struct {
	File   *struct{ Path string } `json:"file_arg"`
	Path   *struct{ Path string } `json:"path_arg"`
	String *string                `json:"string_arg"`
	Uint   *uint64                `json:"uint_arg"`
}

func (a tgArg) path() string {
	switch {
	case a.File != nil:
		return a.File.Path
	case a.Path != nil:
		return a.Path.Path
	case a.String != nil:
		return *a.String
	}
	return ""
}

type tgEvent struct {
	Exec *struct {
		Process tgProcess  `json:"process"`
		Parent  *tgProcess `json:"parent"`
	} `json:"process_exec"`
	Kprobe *struct {
		Process  tgProcess  `json:"process"`
		Parent   *tgProcess `json:"parent"`
		Function string     `json:"function_name"`
		Args     []tgArg    `json:"args"`
		Action   string     `json:"action"`
	} `json:"process_kprobe"`
	Node string    `json:"node_name"`
	Time time.Time `json:"time"`
}

var fileKinds = map[string]string{
	"security_file_permission": "write",
	"security_path_truncate":   "truncate",
	"security_path_unlink":     "delete",
	"security_path_chmod":      "chmod",
}

// Parse turns one line of Tetragon's JSON export into an Event. ok is false for
// lines that are not about a pod's process or file (exits, the host, noise).
func Parse(line []byte) (e Event, ok bool) {
	var t tgEvent
	if json.Unmarshal(line, &t) != nil {
		return e, false
	}
	var p tgProcess
	var parent *tgProcess
	switch {
	case t.Exec != nil:
		p, parent = t.Exec.Process, t.Exec.Parent
		e.Kind = "exec"
		if strings.HasPrefix(p.Binary, "/proc/self/fd/") { // runc's own init
			if parent == nil || path.Base(parent.Binary) != "runc" || !strings.Contains(" "+parent.Arguments+" ", " exec ") {
				return e, false // a container starting, not someone entering one
			}
			e.Kind = "session" // `kubectl exec` / `orcinus exec` into the container
		}
	case t.Kprobe != nil:
		k := t.Kprobe
		p, parent = k.Process, k.Parent
		var target tgArg
		if len(k.Args) > 0 {
			target = k.Args[0]
		}
		if k.Action == "KPROBE_ACTION_SIGKILL" {
			e.Kind = "blocked"
		} else if e.Kind = fileKinds[k.Function]; e.Kind == "" {
			return e, false
		}
		if e.Kind == "delete" && len(k.Args) > 1 {
			target = k.Args[1] // arg 0 is the directory
		}
		e.Path = target.path()
		// The overlay filesystem copying a file up before its first write.
		if strings.Contains(e.Path, "/io.containerd.snapshotter.") {
			return e, false
		}
		if e.Kind == "chmod" && len(k.Args) > 1 && k.Args[1].Uint != nil {
			e.Mode = fmt.Sprintf("%04o", *k.Args[1].Uint&0o7777)
		}
	default:
		return e, false
	}
	if p.Pod == nil {
		return e, false
	}
	e.Time, e.Node = t.Time, t.Node
	e.Namespace, e.Pod = p.Pod.Namespace, p.Pod.Name
	e.Container, e.Image = p.Pod.Container.Name, p.Pod.Container.Image.Name
	e.Service = p.Pod.Labels["io.kompose.service"]
	if e.Kind != "session" {
		e.Binary, e.Args = p.Binary, p.Arguments
	}
	e.UID = p.UID
	if parent != nil && e.Kind != "session" {
		e.Parent = parent.Binary
	}
	return e, true
}

// Filter picks the events to show.
type Filter struct {
	Kind      string // "" (everything) | exec | files
	Namespace string
	Service   string // compose service, or the pod's workload name
}

func (f Filter) match(e Event) bool {
	switch f.Kind {
	case "exec":
		if e.FileEvent() {
			return false
		}
	case "files":
		if !e.FileEvent() {
			return false
		}
	}
	if f.Namespace != "" && e.Namespace != f.Namespace {
		return false
	}
	return f.Service == "" || e.Service == f.Service || strings.HasPrefix(e.Pod, f.Service+"-")
}

func clientset(kubeconfig string) (*kubernetes.Clientset, error) {
	cfg, err := deploy.LoadRESTConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// agents lists the Tetragon agent pods, one per node.
func agents(ctx context.Context, cs kubernetes.Interface) ([]corev1.Pod, error) {
	pods, err := cs.CoreV1().Pods(plugin.TetragonNamespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=tetragon"})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("runtime monitoring is not installed: run `orcinus monitor enable`")
	}
	return pods.Items, nil
}

// Read calls fn for each event since `since` ago, oldest first; with follow it
// keeps going with new events until ctx ends. Events come from every node.
func Read(ctx context.Context, kubeconfig string, since time.Duration, follow bool, f Filter, fn func(Event)) error {
	cs, err := clientset(kubeconfig)
	if err != nil {
		return err
	}
	pods, err := agents(ctx, cs)
	if err != nil {
		return err
	}
	secs := int64(since.Seconds())
	opts := &corev1.PodLogOptions{Container: "export-stdout", Follow: follow}
	if secs > 0 {
		opts.SinceSeconds = &secs
	}
	// The agents' export file outlives their pods and is replayed from the
	// start, so log timestamps say nothing: cut on the event's own time.
	var from time.Time
	if since > 0 {
		from = time.Now().Add(-since)
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		events   []Event
	)
	for _, p := range pods {
		wg.Add(1)
		go func(p corev1.Pod) {
			defer wg.Done()
			err := readAgent(ctx, cs, p.Name, opts, func(e Event) {
				if !f.match(e) || e.Time.Before(from) {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if follow {
					fn(e)
				} else {
					events = append(events, e)
				}
			})
			mu.Lock()
			if err != nil && firstErr == nil && ctx.Err() == nil {
				firstErr = fmt.Errorf("node agent %s: %w", p.Name, err)
			}
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	for _, e := range events {
		fn(e)
	}
	return firstErr
}

func readAgent(ctx context.Context, cs kubernetes.Interface, pod string, opts *corev1.PodLogOptions, fn func(Event)) error {
	stream, err := cs.CoreV1().Pods(plugin.TetragonNamespace).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	return scan(stream, fn)
}

func scan(r io.Reader, fn func(Event)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if e, ok := Parse(sc.Bytes()); ok {
			fn(e)
		}
	}
	return sc.Err()
}

// BlockShellPolicy is the name of the TracingPolicy that kills shells.
const BlockShellPolicy = "orcinus-block-shell"

// Blocked are the programs block-shell kills, matched on the end of the path a
// process asks to run: shells, and package managers (installing tools into a
// running container is the next step of most intrusions).
var Blocked = []string{
	"/sh", "/bash", "/dash", "/ash", "/zsh", "/ksh", "/csh", "/tcsh", "/fish", "/busybox",
	"/apt", "/apt-get", "/dpkg", "/apk", "/dnf", "/yum", "/microdnf", "/rpm",
}

// ShellPolicy builds the block-shell TracingPolicy. It applies only to pods
// orcinus deployed (their app.kubernetes.io/managed-by=orcinus label), so the
// cluster's own pods and plugins — some of which run shell scripts — are never
// touched. Projects and namespaces narrow it further.
func ShellPolicy(projects, namespaces []string) *unstructured.Unstructured {
	exprs := []any{map[string]any{"key": compose.LabelManagedBy, "operator": "In", "values": []any{compose.ManagedByValue}}}
	if len(projects) > 0 {
		exprs = append(exprs, map[string]any{"key": compose.LabelProject, "operator": "In", "values": strs(projects)})
	}
	if len(namespaces) > 0 {
		exprs = append(exprs, map[string]any{"key": "k8s:io.kubernetes.pod.namespace", "operator": "In", "values": strs(namespaces)})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cilium.io/v1alpha1",
		"kind":       "TracingPolicy",
		"metadata":   map[string]any{"name": BlockShellPolicy},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchExpressions": exprs},
			"kprobes": []any{map[string]any{
				// execve's own argument, not the resolved file: on busybox
				// images /bin/cat is busybox too and must keep working.
				"call":    "sys_execve",
				"syscall": true,
				"args":    []any{map[string]any{"index": int64(0), "type": "string"}},
				"selectors": []any{map[string]any{
					"matchArgs":    []any{map[string]any{"index": int64(0), "operator": "Postfix", "values": strs(Blocked)}},
					"matchActions": []any{map[string]any{"action": "Sigkill"}},
				}},
			}},
		},
	}}
}

func strs(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func applier(kubeconfig string) (*deploy.Applier, error) {
	cfg, err := deploy.LoadRESTConfig(kubeconfig)
	if err != nil {
		return nil, err
	}
	return deploy.NewApplier(cfg)
}

// BlockShell turns shell blocking on (for the given scope) or off.
func BlockShell(ctx context.Context, kubeconfig string, on bool, projects, namespaces []string) error {
	if !plugin.Installed("tetragon") {
		if !on {
			return nil
		}
		return fmt.Errorf("runtime monitoring is not installed: run `orcinus monitor enable` first")
	}
	a, err := applier(kubeconfig)
	if err != nil {
		return err
	}
	pol := []runtime.Object{ShellPolicy(projects, namespaces)}
	if !on {
		return a.DeleteObjects(ctx, pol)
	}
	_, err = a.Apply(ctx, pol, deploy.ApplyOptions{})
	return err
}

// Status is what `orcinus monitor status` reports.
type Status struct {
	Agents, Ready int
	Policies      []string // TracingPolicies loaded in the cluster
}

// GetStatus reads the agents' readiness and the loaded policies.
func GetStatus(ctx context.Context, kubeconfig string) (Status, error) {
	var s Status
	cs, err := clientset(kubeconfig)
	if err != nil {
		return s, err
	}
	pods, err := agents(ctx, cs)
	if err != nil {
		return s, err
	}
	for _, p := range pods {
		s.Agents++
		ready := p.Status.Phase == corev1.PodRunning
		for _, c := range p.Status.ContainerStatuses {
			ready = ready && c.Ready
		}
		if ready {
			s.Ready++
		}
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
		} `json:"items"`
	}
	raw, err := cs.RESTClient().Get().AbsPath("/apis/cilium.io/v1alpha1/tracingpolicies").DoRaw(ctx)
	if err != nil {
		return s, fmt.Errorf("list tracing policies: %w", err)
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return s, err
	}
	for _, i := range list.Items {
		s.Policies = append(s.Policies, i.Metadata.Name)
	}
	sort.Strings(s.Policies)
	return s, nil
}

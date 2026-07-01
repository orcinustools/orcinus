package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/biznetgio/orcinus/pkg/compose"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// serviceLabel is the per-service label the kompose fork stamps on pods.
const serviceLabel = "io.kompose.service"

// PodInfo is a compact view of a pod for `orcinus ps`.
type PodInfo struct {
	Name      string
	Namespace string
	Service   string
	Ready     string
	Status    string
	Restarts  int32
	Node      string
}

// ListProjectPods returns the pods belonging to a project (backs `orcinus ps`).
// An empty namespace lists across all namespaces.
func (a *Applier) ListProjectPods(ctx context.Context, project, namespace string) ([]PodInfo, error) {
	if project == "" {
		return nil, fmt.Errorf("project name is required")
	}
	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, compose.LabelProject, project)
	return a.listPods(ctx, namespace, selector)
}

func (a *Applier) listPods(ctx context.Context, namespace, selector string) ([]PodInfo, error) {
	list, err := a.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	out := make([]PodInfo, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		ready := 0
		var restarts int32
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Ready {
				ready++
			}
			restarts += cs.RestartCount
		}
		out = append(out, PodInfo{
			Name:      p.Name,
			Namespace: p.Namespace,
			Service:   p.Labels[serviceLabel],
			Ready:     fmt.Sprintf("%d/%d", ready, len(p.Spec.Containers)),
			Status:    podStatus(p),
			Restarts:  restarts,
			Node:      p.Spec.NodeName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// podStatus mirrors kubectl's headline status column closely enough for a summary.
func podStatus(p *corev1.Pod) string {
	if p.DeletionTimestamp != nil {
		return "Terminating"
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
	}
	if p.Status.Reason != "" {
		return p.Status.Reason
	}
	return string(p.Status.Phase)
}

// StreamServiceLogs streams logs of all pods of a service to out. If project is
// non-empty it further scopes the selection. With follow, pod streams run
// concurrently, each line prefixed with the pod name.
func (a *Applier) StreamServiceLogs(ctx context.Context, service, project, namespace string, follow bool, out io.Writer) error {
	if service == "" {
		return fmt.Errorf("service name is required")
	}
	selector := fmt.Sprintf("%s=%s,%s=%s",
		compose.LabelManagedBy, compose.ManagedByValue, serviceLabel, service)
	if project != "" {
		selector += fmt.Sprintf(",%s=%s", compose.LabelProject, project)
	}

	pods, err := a.listPods(ctx, namespace, selector)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return fmt.Errorf("no pods found for service %q", service)
	}

	prefix := len(pods) > 1
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, pod := range pods {
		if !follow {
			if err := a.streamOne(ctx, pod, follow, prefix, out, &mu); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		wg.Add(1)
		go func(pod PodInfo) {
			defer wg.Done()
			if err := a.streamOne(ctx, pod, follow, prefix, out, &mu); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}(pod)
	}
	wg.Wait()
	return firstErr
}

func (a *Applier) streamOne(ctx context.Context, pod PodInfo, follow, prefix bool, out io.Writer, mu *sync.Mutex) error {
	req := a.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Follow: follow})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("logs for %s: %w", pod.Name, err)
	}
	defer stream.Close()

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		mu.Lock()
		if prefix {
			fmt.Fprintf(out, "[%s] %s\n", pod.Name, sc.Text())
		} else {
			fmt.Fprintln(out, sc.Text())
		}
		mu.Unlock()
	}
	return sc.Err()
}

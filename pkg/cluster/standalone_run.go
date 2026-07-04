//go:build standalone

package cluster

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/orcinustools/orcinus/pkg/runtime"
)

// The standalone provider runs the built-in Kubernetes runtime as a native host
// process — no container runtime required. It needs root (the server manages
// cgroups, iptables and mounts) and a systemd/cgroup-delegated host.
//
// State layout under ~/.orcinus/runtime/<name>/:
//   data/    runtime data-dir (server/node-token lives here)
//   k3s.log  combined server log
//   pid      server process id (its own process group)

func standaloneDir(name string) string { return filepath.Join(Dir(), "runtime", name) }

func initStandalone(o InitOptions) (*InitResult, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("--runtime standalone must run as root (the native runtime manages cgroups/iptables/mounts)")
	}
	bin, err := runtime.ExtractPath()
	if err != nil {
		return nil, fmt.Errorf("extract standalone runtime: %w", err)
	}

	dir := standaloneDir(o.Name)
	dataDir := filepath.Join(dir, "data")
	logPath := filepath.Join(dir, "k3s.log")
	pidPath := filepath.Join(dir, "pid")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	// If a server is already running for this name, reuse it (idempotent).
	if standaloneRunning(o.Name) {
		if st, err := LoadState(); err == nil && st.Runtime == "standalone" {
			return st, nil
		}
	}

	bind := o.BindAddress
	if bind == "" {
		bind = "127.0.0.1"
	}
	args := []string{
		"server",
		"--write-kubeconfig=" + o.KubeconfigPath,
		"--write-kubeconfig-mode=644",
		"--data-dir=" + dataDir,
		fmt.Sprintf("--https-listen-port=%d", o.APIPort),
	}
	for _, san := range tlsSANs(bind, o.Advertise) {
		args = append(args, "--tls-san="+san)
	}
	if o.Token != "" {
		args = append(args, "--token="+o.Token)
	}
	if o.ClusterInit {
		args = append(args, "--cluster-init")
	}
	if o.DatastoreEndpoint != "" {
		args = append(args, "--datastore-endpoint="+o.DatastoreEndpoint)
	}
	args = append(args, o.ExtraServerArgs...)

	logf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command(bin, args...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	// New process group so the server survives this short-lived CLI invocation
	// and can be signalled as a group on `down`.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start standalone runtime: %w", err)
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", pid)), 0o644)
	// Detach: we do not wait on it; it is reparented to init when we exit.
	_ = cmd.Process.Release()

	// Wait for the node to become Ready (or the process to die).
	if err := standaloneWaitReady(o.Name, o.KubeconfigPath, pid, 180*time.Second); err != nil {
		return nil, err
	}

	// Best-effort: make the bundled metrics-server usable (kubelet self-signed).
	standaloneEnableMetrics(o.KubeconfigPath)

	token, _ := os.ReadFile(filepath.Join(dataDir, "server", "node-token"))
	host := "127.0.0.1"
	if o.Advertise != "" {
		host = o.Advertise
	}
	res := &InitResult{
		Name:           o.Name,
		Image:          "standalone:" + runtimeTag(bin),
		ServerURL:      fmt.Sprintf("https://%s:%d", host, o.APIPort),
		Token:          strings.TrimSpace(string(token)),
		APIPort:        o.APIPort,
		KubeconfigPath: o.KubeconfigPath,
		Runtime:        "standalone",
	}
	if err := saveState(res); err != nil {
		return nil, err
	}
	return res, nil
}

// joinStandalone joins this host to an existing cluster natively (no container
// runtime): it runs `k3s agent` (worker) or `k3s server --server` (extra master)
// as a managed background process, and records local state so `cluster down`
// stops it. One standalone node per host.
func joinStandalone(o JoinOptions) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("--runtime standalone must run as root (the native runtime manages cgroups/iptables/mounts)")
	}
	bin, err := runtime.ExtractPath()
	if err != nil {
		return fmt.Errorf("extract standalone runtime: %w", err)
	}
	if standaloneRunning(o.Name) {
		return fmt.Errorf("a standalone node %q is already running here; run `orcinus cluster down` first", o.Name)
	}
	dir := standaloneDir(o.Name)
	dataDir := filepath.Join(dir, "data")
	logPath := filepath.Join(dir, "k3s.log")
	pidPath := filepath.Join(dir, "pid")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}

	role := "agent"
	if o.Role == "server" {
		role = "server" // extra control-plane node (needs an HA datastore cluster)
	}
	args := []string{role, "--server", o.ServerURL, "--token", o.Token,
		"--data-dir", dataDir, "--node-name", o.Name}

	logf, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start standalone %s: %w", role, err)
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", pid)), 0o644)
	_ = cmd.Process.Release()

	// A joining node has no local API to poll; confirm the process stays up.
	for i := 0; i < 10; i++ {
		if !processAlive(pid) {
			return fmt.Errorf("standalone %s exited during join; see %s", role, logPath)
		}
		time.Sleep(time.Second)
	}

	return saveState(&InitResult{
		Name:      o.Name,
		Image:     "standalone:" + runtimeTag(bin),
		ServerURL: o.ServerURL,
		Token:     o.Token,
		Runtime:   "standalone",
	})
}

func downStandalone(name string) (int, error) {
	dir := standaloneDir(name)
	removed := 0
	if pid, ok := readPID(filepath.Join(dir, "pid")); ok {
		// Signal the whole process group; escalate to KILL if it lingers.
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		for i := 0; i < 20 && processAlive(pid); i++ {
			time.Sleep(500 * time.Millisecond)
		}
		if processAlive(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		removed = 1
	}
	// containerd shims / pause processes are spawned in their own process groups,
	// so the group kill above misses them; reap anything referencing the data-dir.
	killProcessesReferencing(dir)
	// The native runtime leaves mounts under the data-dir and /run/k3s; unmount
	// before removal (processes must be gone first so the mounts are idle).
	unmountUnder(dir)
	unmountUnder("/run/k3s")
	// Remove the CNI interfaces and kube/flannel iptables rules the native
	// runtime created (leaving other rules, e.g. Docker, intact).
	cleanupHostNetwork()
	_ = os.RemoveAll(dir)
	_ = os.Remove(filepath.Dir(dir)) // remove the parent runtime/ dir if now empty
	_ = os.Remove(KubeconfigPath())
	_ = os.Remove(statePath())
	return removed, nil
}

func standaloneRunning(name string) bool {
	pid, ok := readPID(filepath.Join(standaloneDir(name), "pid"))
	return ok && processAlive(pid)
}

func standaloneNodes(kubeconfig string) string {
	bin, err := runtime.ExtractPath()
	if err != nil {
		return ""
	}
	out, err := exec.Command(bin, "kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "-o", "wide").CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

// --- helpers ---

func standaloneWaitReady(name, kubeconfig string, pid int, timeout time.Duration) error {
	bin, err := runtime.ExtractPath()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return fmt.Errorf("standalone runtime exited during startup; see %s", filepath.Join(standaloneDir(name), "k3s.log"))
		}
		out, err := exec.Command(bin, "kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "--no-headers").CombinedOutput()
		if err == nil && strings.Contains(string(out), " Ready") {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("standalone cluster %q did not become ready within %s", name, timeout)
}

func standaloneEnableMetrics(kubeconfig string) {
	bin, err := runtime.ExtractPath()
	if err != nil {
		return
	}
	kc := func(args ...string) (string, error) {
		out, err := exec.Command(bin, append([]string{"kubectl", "--kubeconfig", kubeconfig}, args...)...).CombinedOutput()
		return string(out), err
	}
	for i := 0; i < 20; i++ {
		if out, err := kc("-n", "kube-system", "get", "deploy", "metrics-server"); err == nil && strings.Contains(out, "metrics-server") {
			_, _ = kc("-n", "kube-system", "patch", "deployment", "metrics-server", "--type=json",
				"-p", `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`)
			return
		}
		time.Sleep(3 * time.Second)
	}
}

func readPID(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// killProcessesReferencing SIGKILLs every process whose command line mentions
// dir — the runtime's containerd shims and pause containers, which outlive the
// server's process group. Best-effort.
func killProcessesReferencing(dir string) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	self := os.Getpid()
	killed := false
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		if strings.Contains(strings.ReplaceAll(string(b), "\x00", " "), dir) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			killed = true
		}
	}
	if killed {
		time.Sleep(time.Second) // let mounts release before we unmount
	}
}

// cleanupHostNetwork removes the CNI interfaces and kube/flannel/CNI iptables
// rules the native runtime created, preserving all other rules (e.g. Docker).
// Best-effort — mirrors what k3s-killall.sh does. On a real standalone host
// (no other container runtime) this leaves the host as it found it.
func cleanupHostNetwork() {
	for _, ifn := range []string{"cni0", "flannel.1", "flannel-v6.1", "kube-ipvs0", "kube-dummy-if"} {
		_ = exec.Command("ip", "link", "delete", ifn).Run()
	}
	// Drop only kube/flannel/CNI iptables rules; everything else is preserved.
	_ = exec.Command("sh", "-c",
		"iptables-save 2>/dev/null | grep -vE 'KUBE-|CNI-|FLANNEL|flannel|cali-' | iptables-restore 2>/dev/null").Run()
}

// unmountUnder lazily unmounts every mountpoint nested under dir (the native
// runtime creates kubelet/netns mounts there). Best-effort.
func unmountUnder(dir string) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return
	}
	defer f.Close()
	var points []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// mountinfo: field 5 is the mount point.
		if len(fields) >= 5 {
			mp := fields[4]
			if mp == dir || strings.HasPrefix(mp, dir+"/") {
				points = append(points, mp)
			}
		}
	}
	// Unmount deepest first.
	for i := len(points) - 1; i >= 0; i-- {
		_ = syscall.Unmount(points[i], syscall.MNT_DETACH)
	}
}

// runtimeTag returns a short identifier for the extracted runtime (its filename).
func runtimeTag(bin string) string { return filepath.Base(bin) }

// StandaloneKubectl returns the standalone runtime's kubectl entrypoint (the runtime
// binary is multicall: `<bin> kubectl ...`). ok is false if not compiled in.
func StandaloneKubectl() (bin string, ok bool) {
	p, err := runtime.ExtractPath()
	if err != nil {
		return "", false
	}
	return p, true
}

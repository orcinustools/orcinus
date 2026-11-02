//go:build standalone

package cluster

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"time"

	"github.com/orcinustools/orcinus/pkg/runtime"
)

// upgradeStandalone restarts this host's native node on newer runtime binaries,
// with the arguments it runs with now. The embedded binary is the target; the
// intermediate minors a server must pass through are downloaded from the
// upstream release (checksum-verified). Running pods keep running across the
// restarts, as with any runtime restart.
func upgradeStandalone(name string) (string, error) {
	if os.Geteuid() != 0 {
		return "", fmt.Errorf("upgrading the standalone runtime must run as root")
	}
	dir := standaloneDir(name)
	pidPath := filepath.Join(dir, "pid")
	pid, ok := readPID(pidPath)
	if !ok || !processAlive(pid) {
		return "", fmt.Errorf("standalone node %q is not running", name)
	}
	args, err := loadArgs(name)
	if err != nil {
		return "", err
	}
	prev, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	prev = strings.TrimSuffix(prev, " (deleted)")
	cur, err := binVersion(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	steps, err := upgradeSteps(cur)
	if err != nil || len(steps) == 0 {
		return cur, err
	}
	server := len(args) > 0 && args[0] == "server"
	kubeconfig := KubeconfigPath()
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--write-kubeconfig="); ok {
			kubeconfig = v
		}
	}
	if server {
		if out, err := controlPlanes(kubeconfig); err != nil {
			return "", err
		} else if n := len(strings.Split(strings.TrimSpace(out), "\n")); n > 1 {
			return "", fmt.Errorf("the cluster has %d control-plane nodes; --upgrade handles single-server clusters only", n)
		}
	} else {
		steps = steps[len(steps)-1:] // a worker may jump
	}

	for _, v := range steps {
		bin, err := k3sBinary(v) // fetch first: the node is down only from here on
		if err != nil {
			return "", err
		}
		fmt.Fprintf(os.Stderr, "%s: %s → %s\n", name, cur, v)
		stopGroup(pid)
		if pid, err = startNative(dir, bin, args); err == nil {
			if server {
				err = waitNativeVersion(kubeconfig, pid, v, 5*time.Minute)
			} else {
				for i := 0; i < 10 && err == nil; i++ {
					if time.Sleep(time.Second); !processAlive(pid) {
						err = fmt.Errorf("node exited after the upgrade")
					}
				}
			}
		}
		if err != nil {
			stopGroup(pid)
			if p, rerr := startNative(dir, prev, args); rerr == nil {
				pid = p
				err = fmt.Errorf("%w\n(restarted on %s)", err, cur)
			}
			return "", fmt.Errorf("upgrade to %s: %w; see %s", v, err, filepath.Join(dir, "k3s.log"))
		}
		prev, cur = bin, v
	}
	if st, err := LoadState(); err == nil && st.Name == name {
		st.Image = "standalone:" + cur
		_ = saveState(st)
	}
	return cur, nil
}

// The runtime hides its arguments from /proc (they hold the token), so init and
// join keep a copy for restarts.
func saveArgs(dir string, args []string) {
	b, _ := json.Marshal(args)
	_ = os.WriteFile(filepath.Join(dir, "args.json"), b, 0o600)
}

// loadArgs returns the arguments a node was started with. Nodes made before
// they were saved get them rebuilt from the cluster state, which covers every
// default `cluster init`/`join`; extra server args are not recoverable.
func loadArgs(name string) ([]string, error) {
	dir := standaloneDir(name)
	var args []string
	if b, err := os.ReadFile(filepath.Join(dir, "args.json")); err == nil {
		return args, json.Unmarshal(b, &args)
	}
	st, err := LoadState()
	if err != nil {
		return nil, fmt.Errorf("no saved runtime arguments or cluster state for %q: %w", name, err)
	}
	data := filepath.Join(dir, "data")
	if st.KubeconfigPath == "" { // a joined node
		role := "agent"
		if _, err := os.Stat(filepath.Join(data, "server")); err == nil {
			role = "server"
		}
		args = []string{role, "--server", st.ServerURL, "--token", st.Token, "--data-dir", data, "--node-name", name}
	} else {
		args = []string{"server", "--write-kubeconfig=" + st.KubeconfigPath, "--write-kubeconfig-mode=644",
			"--data-dir=" + data, fmt.Sprintf("--https-listen-port=%d", st.APIPort)}
		if u, err := url.Parse(st.ServerURL); err == nil {
			for _, san := range tlsSANs("", u.Hostname()) {
				args = append(args, "--tls-san="+san)
			}
		}
		// sqlite (the default) or embedded etcd; --cluster-init on sqlite data
		// would migrate it to etcd.
		if _, err := os.Stat(filepath.Join(data, "server", "db", "state.db")); err != nil {
			if _, err := os.Stat(filepath.Join(data, "server", "db", "etcd", "member")); err != nil {
				return nil, fmt.Errorf("cannot rebuild the runtime arguments of %q (external datastore?); restart it on the new version by hand", name)
			}
			args = append(args, "--cluster-init")
		}
	}
	fmt.Fprintf(os.Stderr, "note: runtime arguments rebuilt from cluster state (extra server args, if any, are not kept)\n")
	saveArgs(dir, args)
	return args, nil
}

func startNative(dir, bin string, args []string) (int, error) {
	logf, err := os.OpenFile(filepath.Join(dir, "k3s.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(fmt.Sprintf("%d\n", pid)), 0o644)
	_ = cmd.Process.Release()
	return pid, nil
}

func controlPlanes(kubeconfig string) (string, error) {
	bin, err := runtime.ExtractPath()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(bin, "kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "-l", "node-role.kubernetes.io/control-plane",
		"-o", `jsonpath={range .items[*]}{.status.nodeInfo.kubeletVersion} {.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}`).Output() // stdout only: kubectl warns about version skew mid-upgrade
	if err != nil {
		return "", fmt.Errorf("list control-plane nodes: %w", err)
	}
	return string(out), nil
}

func waitNativeVersion(kubeconfig string, pid int, v string, timeout time.Duration) error {
	var got string
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		if !processAlive(pid) {
			return fmt.Errorf("runtime exited during startup")
		}
		got, _ = controlPlanes(kubeconfig)
		if strings.TrimSpace(got) == v+" True" {
			return nil
		}
	}
	return fmt.Errorf("node did not come up Ready on %s within %s (last: %q)", v, timeout, strings.TrimSpace(got))
}

// binVersion runs `<bin> --version` ("k3s version v1.31.5+k3s1 (abc)").
func binVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").Output()
	if f := strings.Fields(string(out)); err == nil && len(f) >= 3 {
		return f[2], nil
	}
	return "", fmt.Errorf("read runtime version of %s: %v", bin, err)
}

// k3sBinary returns a runtime binary of version v: the embedded one when it is
// that version, else the upstream release asset, verified against its checksums.
func k3sBinary(v string) (string, error) {
	embedded, err := runtime.ExtractPath()
	if err != nil {
		return "", err
	}
	if ev, _ := binVersion(embedded); ev == v {
		return embedded, nil
	}
	a, ok := map[string][2]string{ // release asset, checksum file
		"amd64": {"k3s", "sha256sum-amd64.txt"},
		"arm64": {"k3s-arm64", "sha256sum-arm64.txt"},
		"arm":   {"k3s-armhf", "sha256sum-arm.txt"},
	}[goruntime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no upstream runtime for %s", goruntime.GOARCH)
	}
	asset, sums := a[0], a[1]
	path := filepath.Join(filepath.Dir(embedded), "k3s-"+v)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	base := "https://github.com/k3s-io/k3s/releases/download/" + url.PathEscape(v) + "/"
	fmt.Fprintf(os.Stderr, "downloading runtime %s...\n", v)

	resp, err := http.Get(base + sums)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var want string
	for sc := bufio.NewScanner(resp.Body); sc.Scan(); {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("no checksum for %s in %s%s", asset, base, sums)
	}

	bresp, err := http.Get(base + asset)
	if err != nil {
		return "", err
	}
	defer bresp.Body.Close()
	if bresp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s%s: %s", base, asset, bresp.Status)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), bresp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = fmt.Errorf("checksum mismatch for %s%s", base, asset)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// Package cluster manages the orcinus cluster runtime for `orcinus init` and
// `orcinus join`. It provisions a lightweight single-node Kubernetes runtime in a
// container, writes a kubeconfig, and records cluster state so subsequent
// commands (deploy/ls/ps/rm) work with no extra configuration.
//
// The container image is a configurable constant; the docker command is taken
// from $ORCINUS_DOCKER (default "docker").
package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultImage is the runtime image used for the embedded cluster.
const DefaultImage = "rancher/k3s:v1.31.5-k3s1"

// DefaultName is the default server container name.
const DefaultName = "orcinus"

// InitOptions configures `orcinus init`.
type InitOptions struct {
	Name              string
	Image             string
	APIPort           int      // host port mapped to the in-container API (6443)
	Token             string   // optional fixed join token
	ClusterInit       bool     // embedded etcd (HA)
	DatastoreEndpoint string   // external datastore
	ExtraServerArgs   []string // additional runtime server args
	KubeconfigPath    string   // where to write the kubeconfig (default: ~/.orcinus/kubeconfig)
}

// InitResult is returned after a successful init.
type InitResult struct {
	Name           string `json:"name"`
	Image          string `json:"image"`
	ServerURL      string `json:"server"` // URL other nodes use to join
	Token          string `json:"token"`
	APIPort        int    `json:"apiPort"`
	KubeconfigPath string `json:"kubeconfig"`
}

// JoinOptions configures `orcinus join`.
type JoinOptions struct {
	Name      string
	Image     string
	ServerURL string
	Token     string
}

// Init provisions a single-node cluster and writes kubeconfig + state.
func Init(o InitOptions) (*InitResult, error) {
	if o.Name == "" {
		o.Name = DefaultName
	}
	if o.Image == "" {
		o.Image = DefaultImage
	}
	if o.APIPort == 0 {
		o.APIPort = 6443
	}
	if o.KubeconfigPath == "" {
		o.KubeconfigPath = KubeconfigPath()
	}

	// Idempotency: reuse an already-running cluster of the same name; refuse a
	// stopped one so state is never silently inconsistent.
	exists, running := containerState(o.Name)
	if exists && !running {
		return nil, fmt.Errorf("a cluster named %q already exists but is not running; run `orcinus down` first", o.Name)
	}
	if !exists {
		args := []string{
			"run", "-d", "--privileged",
			"--name", o.Name,
			"--label", "orcinus.cluster=" + o.Name,
			"-p", fmt.Sprintf("127.0.0.1:%d:6443", o.APIPort),
			o.Image, "server",
			"--write-kubeconfig-mode=644",
			"--tls-san=127.0.0.1",
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

		if out, err := docker(args...); err != nil {
			return nil, fmt.Errorf("start cluster: %w\n%s", err, out)
		}
	}

	// Wait for the node to become Ready.
	if err := waitReady(o.Name, 180*time.Second); err != nil {
		return nil, err
	}

	// Extract & rewrite kubeconfig for host access.
	raw, err := docker("exec", o.Name, "cat", "/etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w\n%s", err, raw)
	}
	kubeconfig := strings.ReplaceAll(raw, "127.0.0.1:6443", fmt.Sprintf("127.0.0.1:%d", o.APIPort))
	if err := writeFile(o.KubeconfigPath, kubeconfig, 0o600); err != nil {
		return nil, err
	}

	// Read the join token and the server's container IP (for joins).
	token, err := docker("exec", o.Name, "cat", "/var/lib/rancher/k3s/server/node-token")
	if err != nil {
		return nil, fmt.Errorf("read node-token: %w\n%s", err, token)
	}
	ip, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", o.Name)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w\n%s", err, ip)
	}

	res := &InitResult{
		Name:           o.Name,
		Image:          o.Image,
		ServerURL:      fmt.Sprintf("https://%s:6443", strings.TrimSpace(ip)),
		Token:          strings.TrimSpace(token),
		APIPort:        o.APIPort,
		KubeconfigPath: o.KubeconfigPath,
	}
	if err := saveState(res); err != nil {
		return nil, err
	}
	return res, nil
}

// Join starts an agent node that joins an existing cluster. If ServerURL/Token
// are empty, they are read from saved cluster state.
func Join(o JoinOptions) error {
	clusterName := DefaultName
	if st, err := LoadState(); err == nil {
		clusterName = st.Name
		if o.ServerURL == "" {
			o.ServerURL = st.ServerURL
		}
		if o.Token == "" {
			o.Token = st.Token
		}
		if o.Image == "" {
			o.Image = st.Image
		}
		if o.Name == "" {
			o.Name = st.Name + "-agent"
		}
	}
	if o.ServerURL == "" || o.Token == "" {
		return fmt.Errorf("no --server/--token given and no saved cluster state")
	}
	if o.Image == "" {
		o.Image = DefaultImage
	}
	if o.Name == "" {
		o.Name = DefaultName + "-agent"
	}

	args := []string{
		"run", "-d", "--privileged",
		"--name", o.Name,
		"--label", "orcinus.cluster=" + clusterName,
		"-e", "K3S_URL=" + o.ServerURL,
		"-e", "K3S_TOKEN=" + o.Token,
		o.Image, "agent",
	}
	if out, err := docker(args...); err != nil {
		return fmt.Errorf("start agent: %w\n%s", err, out)
	}
	return nil
}

// StatusResult describes the current cluster.
type StatusResult struct {
	State   *InitResult
	Running bool
	Nodes   string // best-effort `kubectl get nodes -o wide` output
}

// Status reports on the orcinus-managed cluster (from saved state).
func Status(name string) (*StatusResult, error) {
	st, err := LoadState()
	if err != nil {
		return nil, fmt.Errorf("no cluster state found; run `orcinus init` first")
	}
	if name == "" {
		name = st.Name
	}
	_, running := containerState(name)
	res := &StatusResult{State: st, Running: running}
	if running {
		if out, err := docker("exec", name, "kubectl", "get", "nodes", "-o", "wide"); err == nil {
			res.Nodes = out
		}
	}
	return res, nil
}

// Down stops and removes the cluster (server + all joined nodes) and clears the
// saved kubeconfig/state.
func Down(name string) (int, error) {
	if name == "" {
		if st, err := LoadState(); err == nil {
			name = st.Name
		} else {
			name = DefaultName
		}
	}

	removed := 0
	// Remove every container labeled for this cluster (server + agents).
	if ids, err := docker("ps", "-aq", "--filter", "label=orcinus.cluster="+name); err == nil {
		for _, id := range strings.Fields(ids) {
			if _, err := docker("rm", "-f", id); err == nil {
				removed++
			}
		}
	}
	// Fallback for clusters created before labels existed.
	_, _ = docker("rm", "-f", name)

	_ = os.Remove(KubeconfigPath())
	_ = os.Remove(statePath())
	return removed, nil
}

// containerState reports whether a container exists and whether it is running.
func containerState(name string) (exists, running bool) {
	out, err := docker("inspect", "-f", "{{.State.Running}}", name)
	if err != nil {
		return false, false
	}
	return true, strings.TrimSpace(out) == "true"
}

// --- state & paths ---

// Dir is the orcinus state directory (~/.orcinus).
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".orcinus"
	}
	return filepath.Join(home, ".orcinus")
}

// KubeconfigPath is where the cluster kubeconfig is written.
func KubeconfigPath() string { return filepath.Join(Dir(), "kubeconfig") }

func statePath() string { return filepath.Join(Dir(), "cluster.json") }

func saveState(r *InitResult) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(statePath(), string(b), 0o600)
}

// LoadState reads the saved cluster state written by Init.
func LoadState() (*InitResult, error) {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return nil, err
	}
	var r InitResult
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// --- helpers ---

// dockerArgv returns the docker command, honoring $ORCINUS_DOCKER (e.g. "sudo docker").
func dockerArgv() []string {
	if v := os.Getenv("ORCINUS_DOCKER"); strings.TrimSpace(v) != "" {
		return strings.Fields(v)
	}
	return []string{"docker"}
}

func docker(args ...string) (string, error) {
	argv := append(dockerArgv(), args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func waitReady(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := docker("exec", name, "kubectl", "get", "nodes", "--no-headers")
		if err == nil && strings.Contains(out, " Ready") {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("cluster %q did not become ready within %s", name, timeout)
}

func writeFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), mode)
}

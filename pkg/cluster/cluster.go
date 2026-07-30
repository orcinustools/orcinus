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
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DefaultImage is the runtime image used for the container-based cluster.
const DefaultImage = "rancher/k3s:v1.31.5-k3s1"

// DefaultName is the default server container name.
const DefaultName = "orcinus"

// InitOptions configures `orcinus init`.
type InitOptions struct {
	Name              string
	Image             string
	APIPort           int      // host port mapped to the in-container API (6443)
	HTTPPort          int      // host port → ingress :80 (0 = don't publish)
	HTTPSPort         int      // host port → ingress :443 (0 = don't publish)
	BindAddress       string   // host interface to publish the API port on (default 127.0.0.1)
	Advertise         string   // address other nodes/clients use (TLS SAN, kubeconfig, join)
	Token             string   // optional fixed join token
	ClusterInit       bool     // embedded etcd (HA)
	DatastoreEndpoint string   // external datastore
	ExtraServerArgs   []string // additional runtime server args
	KubeconfigPath    string   // where to write the kubeconfig (default: ~/.orcinus/kubeconfig)
	Runtime           string   // "docker" (default) or "standalone" (native, built-in runtime)
}

// InitResult is returned after a successful init.
type InitResult struct {
	Name           string `json:"name"`
	Image          string `json:"image"`
	ServerURL      string `json:"server"` // URL other nodes use to join
	Token          string `json:"token"`
	APIPort        int    `json:"apiPort"`
	KubeconfigPath string `json:"kubeconfig"`
	Runtime        string `json:"runtime,omitempty"` // "docker" (default) or "standalone"
}

// JoinOptions configures `orcinus join`.
type JoinOptions struct {
	Name      string
	Image     string
	ServerURL string
	Token     string
	Role      string // "agent" (worker, default) or "server" (control-plane/master)
	Runtime   string // "docker" (default) or "standalone" (native, built-in runtime)
	Advertise string // address other nodes reach THIS node on (cross-host docker joins; default: auto-detected)
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
	if o.Runtime == "" {
		o.Runtime = "docker"
	}
	// The standalone runtime runs the built-in Kubernetes server natively on this
	// host (no container runtime). It is a separate provider path.
	if o.Runtime == "standalone" {
		return initStandalone(o)
	}
	if o.Runtime != "docker" {
		return nil, fmt.Errorf("unknown --runtime %q (want: docker|standalone)", o.Runtime)
	}
	if o.BindAddress == "" {
		o.BindAddress = "127.0.0.1"
	}
	// Advertising a reachable address implies listening beyond loopback.
	if o.Advertise != "" && o.BindAddress == "127.0.0.1" {
		o.BindAddress = "0.0.0.0"
	}

	// Idempotency: reuse an already-running cluster of the same name; refuse a
	// stopped one so state is never silently inconsistent.
	exists, running := containerState(o.Name)
	if exists && !running {
		return nil, fmt.Errorf("a cluster named %q already exists but is not running; run `orcinus cluster down` first", o.Name)
	}
	if !exists {
		// Docker run flags, including port publishing.
		runFlags := []string{
			"run", "-d", "--privileged",
			"--name", o.Name,
			"--label", "orcinus.cluster=" + o.Name,
			"-p", fmt.Sprintf("%s:%d:6443", o.BindAddress, o.APIPort),
		}
		if o.HTTPPort > 0 {
			runFlags = append(runFlags, "-p", fmt.Sprintf("0.0.0.0:%d:80", o.HTTPPort))
		}
		if o.HTTPSPort > 0 {
			runFlags = append(runFlags, "-p", fmt.Sprintf("0.0.0.0:%d:443", o.HTTPSPort))
		}
		// An advertised address means nodes on other hosts will join. Those
		// nodes reach this node at the host address, not the container's
		// bridge IP — publish the overlay-network (flannel VXLAN) and kubelet
		// ports so cross-host traffic can get in.
		if o.Advertise != "" {
			runFlags = append(runFlags, "-p", "0.0.0.0:8472:8472/udp", "-p", "0.0.0.0:10250:10250")
		}

		// Runtime server command.
		serverCmd := []string{o.Image, "server", "--write-kubeconfig-mode=644"}
		for _, san := range tlsSANs(o.BindAddress, o.Advertise) {
			serverCmd = append(serverCmd, "--tls-san="+san)
		}
		if o.Advertise != "" {
			ip, err := resolveIPv4(o.Advertise)
			if err != nil {
				return nil, fmt.Errorf("resolve --advertise %q: %w", o.Advertise, err)
			}
			serverCmd = append(serverCmd,
				// The node is reachable from other hosts at the advertised
				// address; the container bridge IP is only valid on this host.
				"--node-external-ip="+ip,
				// Route flannel (VXLAN) between nodes over their external IPs.
				"--flannel-external-ip",
				// kubectl logs/exec: dial kubelets on their external IPs —
				// every node's bridge InternalIP is 172.17.0.x, which
				// collides across hosts.
				"--kube-apiserver-arg=kubelet-preferred-address-types=ExternalIP,InternalIP,Hostname",
			)
			if o.APIPort != 6443 {
				// The kubernetes.default endpoint must carry the *published*
				// port, since in-cluster clients connect via the host.
				serverCmd = append(serverCmd, "--advertise-port="+strconv.Itoa(o.APIPort))
			}
		}
		if o.Token != "" {
			serverCmd = append(serverCmd, "--token="+o.Token)
		}
		if o.ClusterInit {
			serverCmd = append(serverCmd, "--cluster-init")
		}
		if o.DatastoreEndpoint != "" {
			serverCmd = append(serverCmd, "--datastore-endpoint="+o.DatastoreEndpoint)
		}
		serverCmd = append(serverCmd, o.ExtraServerArgs...)

		args := append(runFlags, serverCmd...)
		if out, err := docker(args...); err != nil {
			return nil, fmt.Errorf("start cluster: %w\n%s", err, out)
		}
	}

	// Wait for the node to become Ready.
	if err := waitReady(o.Name, 180*time.Second); err != nil {
		return nil, err
	}

	// On a fresh cluster, make the bundled metrics-server work in the
	// container runtime (best-effort) so `kubectl top` and HPAs get metrics.
	if !exists {
		enableMetrics(o.Name, o.Advertise != "")
	}

	// Extract & rewrite kubeconfig for host access.
	raw, err := docker("exec", o.Name, "cat", "/etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w\n%s", err, raw)
	}
	// kubeconfig points at the advertised address if set, else loopback.
	kcHost := "127.0.0.1"
	if o.Advertise != "" {
		kcHost = o.Advertise
	}
	kubeconfig := strings.ReplaceAll(raw, "127.0.0.1:6443", fmt.Sprintf("%s:%d", kcHost, o.APIPort))
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

	// Join URL: the advertised host:port for remote nodes, else the container's
	// bridge IP (same-host agents only).
	serverURL := fmt.Sprintf("https://%s:6443", strings.TrimSpace(ip))
	if o.Advertise != "" {
		serverURL = fmt.Sprintf("https://%s:%d", o.Advertise, o.APIPort)
	}

	res := &InitResult{
		Name:           o.Name,
		Image:          o.Image,
		ServerURL:      serverURL,
		Token:          strings.TrimSpace(token),
		APIPort:        o.APIPort,
		KubeconfigPath: o.KubeconfigPath,
		Runtime:        "docker",
	}
	if err := saveState(res); err != nil {
		return nil, err
	}
	return res, nil
}

// Join adds a node to an existing cluster. Role "agent" (default) adds a worker;
// role "server" adds a control-plane (master) node — which requires the cluster
// to have been created with an HA datastore (see Init --cluster-init or
// --datastore-endpoint). If ServerURL/Token are empty they come from saved state.
func Join(o JoinOptions) error {
	if o.Role == "" {
		o.Role = "agent"
	}
	if o.Role != "agent" && o.Role != "server" {
		return fmt.Errorf("invalid --role %q (want: agent|server)", o.Role)
	}

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
		if o.Runtime == "" {
			o.Runtime = st.Runtime
		}
	}
	if o.Runtime == "" {
		o.Runtime = "docker"
	}
	if o.ServerURL == "" || o.Token == "" {
		return fmt.Errorf("no --server/--token given and no saved cluster state")
	}
	if o.Name == "" {
		o.Name = clusterName + "-" + o.Role
	}

	// Native (no container runtime) join.
	if o.Runtime == "standalone" {
		return joinStandalone(o)
	}
	if o.Runtime != "docker" {
		return fmt.Errorf("unknown --runtime %q (want: docker|standalone)", o.Runtime)
	}

	if o.Image == "" {
		o.Image = DefaultImage
	}

	// Same-host joins (another node next to an existing one) talk over the
	// docker bridge and need no extra wiring. A join on a host with no node
	// of this cluster yet is a cross-host join: the new node must advertise
	// an address the other hosts can reach, and expose the overlay-network
	// (flannel VXLAN) + kubelet ports on it.
	crossHost := !hasLocalClusterNode(clusterName)
	if crossHost && o.Role == "server" {
		return fmt.Errorf("cross-host control-plane joins are not supported with the docker runtime\n" +
			"(etcd peers would advertise unreachable container IPs) — use the standalone runtime:\n" +
			"  sudo orcinus-standalone cluster join --runtime standalone --role server --server <url> --token <token>")
	}
	if crossHost && o.Advertise == "" {
		ip, err := routeSourceIP(o.ServerURL)
		if err != nil {
			return fmt.Errorf("detect this host's address for cross-host join: %w\n(pass it explicitly with --advertise <this-host-ip>)", err)
		}
		o.Advertise = ip
	}

	base := []string{
		"run", "-d", "--privileged",
		"--name", o.Name,
		"--label", "orcinus.cluster=" + clusterName,
	}
	if crossHost {
		base = append(base, "-p", "0.0.0.0:8472:8472/udp", "-p", "0.0.0.0:10250:10250")
	}
	var args []string
	if o.Role == "server" {
		// Additional control-plane node joins the existing server.
		args = append(base, o.Image, "server",
			"--server", o.ServerURL,
			"--token", o.Token,
		)
	} else {
		args = append(base,
			"-e", "K3S_URL="+o.ServerURL,
			"-e", "K3S_TOKEN="+o.Token,
			o.Image, "agent",
		)
		if crossHost {
			ip, err := resolveIPv4(o.Advertise)
			if err != nil {
				return fmt.Errorf("resolve --advertise %q: %w", o.Advertise, err)
			}
			args = append(args, "--node-external-ip="+ip)
		}
	}
	if out, err := docker(args...); err != nil {
		return fmt.Errorf("start %s node: %w\n%s", o.Role, err, out)
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
		return nil, fmt.Errorf("no cluster state found; run `orcinus cluster init` first")
	}
	if name == "" {
		name = st.Name
	}
	if st.Runtime == "standalone" {
		running := standaloneRunning(name)
		res := &StatusResult{State: st, Running: running}
		if running {
			res.Nodes = standaloneNodes(st.KubeconfigPath)
		}
		return res, nil
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
	st, stErr := LoadState()
	if name == "" {
		if stErr == nil {
			name = st.Name
		} else {
			name = DefaultName
		}
	}
	// Standalone clusters are native host processes, not containers.
	if stErr == nil && st.Runtime == "standalone" {
		return downStandalone(name)
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

// tlsSANs returns the TLS subject-alternative-names for the API server cert:
// always 127.0.0.1, plus the advertised address and a concrete bind address.
func tlsSANs(bind, advertise string) []string {
	sans := []string{"127.0.0.1"}
	add := func(s string) {
		if s == "" || s == "0.0.0.0" {
			return
		}
		for _, e := range sans {
			if e == s {
				return
			}
		}
		sans = append(sans, s)
	}
	add(advertise)
	add(bind)
	return sans
}

// hasLocalClusterNode reports whether a node container of this cluster is
// already running on this host (i.e. a join here is a same-host join).
func hasLocalClusterNode(cluster string) bool {
	out, err := docker("ps", "-q", "--filter", "label=orcinus.cluster="+cluster)
	return err == nil && strings.TrimSpace(out) != ""
}

// routeSourceIP returns the local address the kernel routes through to reach
// the server — the address the rest of the cluster can reach this host on.
func routeSourceIP(serverURL string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("parse server URL %q: %w", serverURL, err)
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return "", fmt.Errorf("no host in server URL %q", serverURL)
	}
	if port == "" {
		port = "6443"
	}
	// UDP dial sends no packets; it only asks the kernel for a route.
	conn, err := net.Dial("udp", net.JoinHostPort(host, port))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return "", fmt.Errorf("no local address on route to %s", host)
	}
	return addr.IP.String(), nil
}

// resolveIPv4 returns s if it is already an IP, else its first IPv4 address.
func resolveIPv4(s string) (string, error) {
	if ip := net.ParseIP(s); ip != nil {
		return s, nil
	}
	ips, err := net.LookupIP(s)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("%q has no IPv4 address", s)
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

// enableMetrics patches the bundled metrics-server to skip kubelet TLS
// verification, which it otherwise can't satisfy inside the container runtime.
// On cross-host clusters (externalFirst) it also flips the kubelet address
// preference to ExternalIP: every node's bridge InternalIP is 172.17.0.x,
// which collides across hosts. Best-effort: never fails the init.
func enableMetrics(name string, externalFirst bool) {
	for i := 0; i < 20; i++ {
		out, err := docker("exec", name, "kubectl", "-n", "kube-system", "get", "deploy", "metrics-server")
		if err == nil && strings.Contains(out, "metrics-server") {
			_, _ = docker("exec", name, "kubectl", "-n", "kube-system", "patch", "deployment", "metrics-server",
				"--type=json",
				"-p", `[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]`)
			if externalFirst {
				patchMetricsAddressTypes(name)
			}
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// patchMetricsAddressTypes replaces metrics-server's existing
// --kubelet-preferred-address-types argument with an ExternalIP-first one.
// The flag accumulates when repeated, so it must be replaced in place.
func patchMetricsAddressTypes(name string) {
	out, err := docker("exec", name, "kubectl", "-n", "kube-system", "get", "deploy", "metrics-server",
		"-o", "jsonpath={.spec.template.spec.containers[0].args}")
	if err != nil {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &args); err != nil {
		return
	}
	patch := func(op string, idx interface{}) string {
		b, _ := json.Marshal([]map[string]interface{}{{
			"op":    op,
			"path":  fmt.Sprintf("/spec/template/spec/containers/0/args/%v", idx),
			"value": "--kubelet-preferred-address-types=ExternalIP,InternalIP,Hostname",
		}})
		return string(b)
	}
	for i, a := range args {
		if strings.HasPrefix(a, "--kubelet-preferred-address-types") {
			_, _ = docker("exec", name, "kubectl", "-n", "kube-system", "patch", "deployment", "metrics-server",
				"--type=json", "-p", patch("replace", i))
			return
		}
	}
	_, _ = docker("exec", name, "kubectl", "-n", "kube-system", "patch", "deployment", "metrics-server",
		"--type=json", "-p", patch("add", "-"))
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

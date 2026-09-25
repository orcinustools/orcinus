package cluster

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UpdateOptions configures `orcinus cluster update`. A nil port leaves that
// port as it is; 0 stops publishing it.
type UpdateOptions struct {
	Name      string
	HTTPPort  *int
	HTTPSPort *int
}

// nodeContainer is the part of `docker inspect` needed to recreate a node.
type nodeContainer struct {
	Config struct {
		Image    string
		Cmd      []string
		Hostname string
		Env      []string
		Labels   map[string]string
	}
	HostConfig struct {
		PortBindings   map[string][]struct{ HostIp, HostPort string }
		DeviceRequests []struct {
			Driver    string
			DeviceIDs []string
		}
		RestartPolicy struct{ Name string }
	}
	Mounts []struct {
		Type, Name, Source, Destination string
	}
}

func inspectNode(name string) (*nodeContainer, error) {
	out, err := docker("inspect", name)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w\n%s", name, err, out)
	}
	var cs []nodeContainer
	if err := json.Unmarshal([]byte(out), &cs); err != nil || len(cs) != 1 {
		return nil, fmt.Errorf("inspect %s: unexpected output", name)
	}
	return &cs[0], nil
}

// publishedPort returns the host port a container port is published on, or 0.
func (c *nodeContainer) publishedPort(port string) int {
	for _, b := range c.HostConfig.PortBindings[port+"/tcp"] {
		var n int
		if _, err := fmt.Sscan(b.HostPort, &n); err == nil {
			return n
		}
	}
	return 0
}

// createArgs rebuilds the `docker create` arguments for the same node, with
// the ingress ports replaced by the given ones (0 = not published).
func (c *nodeContainer) createArgs(name string, httpPort, httpsPort int) []string {
	args := []string{"create", "--privileged", "--name", name, "--hostname", c.Config.Hostname}
	for k, v := range c.Config.Labels {
		if strings.HasPrefix(k, "orcinus.") {
			args = append(args, "--label", k+"="+v)
		}
	}
	for _, e := range c.Config.Env {
		if strings.HasPrefix(e, "K3S_") {
			args = append(args, "-e", e)
		}
	}
	for _, m := range c.Mounts {
		switch m.Type {
		case "volume":
			args = append(args, "-v", m.Name+":"+m.Destination)
		case "bind":
			args = append(args, "-v", m.Source+":"+m.Destination)
		}
	}
	ports := make([]string, 0, len(c.HostConfig.PortBindings))
	for p := range c.HostConfig.PortBindings {
		if p != "80/tcp" && p != "443/tcp" {
			ports = append(ports, p)
		}
	}
	sort.Strings(ports)
	for _, p := range ports {
		for _, b := range c.HostConfig.PortBindings[p] {
			args = append(args, "-p", fmt.Sprintf("%s:%s:%s", b.HostIp, b.HostPort, p))
		}
	}
	if httpPort > 0 {
		args = append(args, "-p", fmt.Sprintf("0.0.0.0:%d:80", httpPort))
	}
	if httpsPort > 0 {
		args = append(args, "-p", fmt.Sprintf("0.0.0.0:%d:443", httpsPort))
	}
	for _, d := range c.HostConfig.DeviceRequests {
		if d.Driver == "cdi" {
			for _, id := range d.DeviceIDs {
				args = append(args, "--device", id)
			}
		}
	}
	if r := c.HostConfig.RestartPolicy.Name; r != "" && r != "no" {
		args = append(args, "--restart", r)
	}
	return append(append(args, c.Config.Image), c.Config.Cmd...)
}

// Update changes the ingress ports of a docker-runtime cluster. Docker cannot
// add ports to an existing container, so the server container is recreated on
// the same volumes and hostname — same node, same data — with /etc/rancher
// (the node password) copied over. If the new one does not come up Ready the
// old one is put back.
func Update(o UpdateOptions) (httpPort, httpsPort int, err error) {
	st, stErr := LoadState()
	if o.Name == "" {
		o.Name = DefaultName
		if stErr == nil {
			o.Name = st.Name
		}
	}
	if stErr == nil && st.Runtime == "standalone" {
		return 0, 0, fmt.Errorf("the standalone runtime serves ingress on the host's own 80/443 already; nothing to update")
	}
	if exists, running := containerState(o.Name); !exists || !running {
		return 0, 0, fmt.Errorf("cluster %q is not running", o.Name)
	}
	c, err := inspectNode(o.Name)
	if err != nil {
		return 0, 0, err
	}
	httpPort, httpsPort = c.publishedPort("80"), c.publishedPort("443")
	if o.HTTPPort != nil {
		httpPort = *o.HTTPPort
	}
	if o.HTTPSPort != nil {
		httpsPort = *o.HTTPSPort
	}
	if httpPort == c.publishedPort("80") && httpsPort == c.publishedPort("443") {
		return httpPort, httpsPort, nil // already so
	}

	tmp, err := os.MkdirTemp("", "orcinus-update")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(tmp)
	if out, err := docker("cp", o.Name+":/etc/rancher", tmp); err != nil {
		return 0, 0, fmt.Errorf("save node identity: %w\n%s", err, out)
	}

	old := o.Name + "-old"
	restore := func(cause error) error {
		_, _ = docker("rm", "-f", o.Name)
		_, _ = docker("rename", old, o.Name)
		_, _ = docker("start", o.Name)
		return fmt.Errorf("%w\n(the previous container was restored)", cause)
	}
	if out, err := docker("stop", o.Name); err != nil {
		return 0, 0, fmt.Errorf("stop cluster: %w\n%s", err, out)
	}
	if out, err := docker("rename", o.Name, old); err != nil {
		_, _ = docker("start", o.Name)
		return 0, 0, fmt.Errorf("rename cluster container: %w\n%s", err, out)
	}
	if out, err := docker(c.createArgs(o.Name, httpPort, httpsPort)...); err != nil {
		return 0, 0, restore(fmt.Errorf("create cluster container: %w\n%s", err, out))
	}
	if out, err := docker("cp", filepath.Join(tmp, "rancher"), o.Name+":/etc/"); err != nil {
		return 0, 0, restore(fmt.Errorf("restore node identity: %w\n%s", err, out))
	}
	if out, err := docker("start", o.Name); err != nil {
		return 0, 0, restore(fmt.Errorf("start cluster: %w\n%s", err, out))
	}
	// waitReady passes once the API answers (the node's Ready status may still be
	// the old one) — enough to catch a server that will not start.
	if err := waitReady(o.Name, 180*time.Second); err != nil {
		return 0, 0, restore(err)
	}
	_, _ = docker("rm", old) // no -v: the volumes now belong to the new container

	advertised := false
	for _, a := range c.Config.Cmd {
		if a == "--flannel-external-ip" {
			applyVXLANChecksumFix(o.Name)
		}
		advertised = advertised || strings.HasPrefix(a, "--node-external-ip=")
	}
	// Without --advertise, joins go to the container's bridge IP, which the new
	// container may not have kept.
	if stErr == nil && st.Name == o.Name && !advertised {
		if ip, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", o.Name); err == nil && strings.TrimSpace(ip) != "" {
			st.ServerURL = fmt.Sprintf("https://%s:6443", strings.TrimSpace(ip))
			_ = saveState(st)
		}
	}
	return httpPort, httpsPort, nil
}

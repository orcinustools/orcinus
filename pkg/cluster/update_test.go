package cluster

import (
	"encoding/json"
	"strings"
	"testing"
)

// A GPU server node as `docker inspect` shows it (trimmed).
const inspectFixture = `[{
 "Config": {"Image": "orcinus/k3s-nvidia:v1.31.5-k3s1", "Hostname": "6a7908878ddc",
   "Cmd": ["server", "--write-kubeconfig-mode=644"],
   "Env": ["PATH=/usr/bin", "K3S_TOKEN=t"],
   "Labels": {"orcinus.cluster": "orcinus", "org.opencontainers.image.version": "24.04"}},
 "HostConfig": {"PortBindings": {"6443/tcp": [{"HostIp": "127.0.0.1", "HostPort": "6443"}],
                                 "80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}]},
   "DeviceRequests": [{"Driver": "cdi", "DeviceIDs": ["nvidia.com/gpu=all"]}],
   "RestartPolicy": {"Name": "no"}},
 "Mounts": [{"Type": "volume", "Name": "v1", "Destination": "/var/lib/rancher/k3s"}]
}]`

func TestNodeCreateArgs(t *testing.T) {
	var cs []nodeContainer
	if err := json.Unmarshal([]byte(inspectFixture), &cs); err != nil {
		t.Fatal(err)
	}
	c := &cs[0]
	if got := c.publishedPort("80"); got != 8080 {
		t.Errorf("publishedPort(80) = %d, want 8080", got)
	}
	got := strings.Join(c.createArgs("orcinus", 80, 443), " ")
	want := "create --privileged --name orcinus --hostname 6a7908878ddc --label orcinus.cluster=orcinus -e K3S_TOKEN=t " +
		"-v v1:/var/lib/rancher/k3s -p 127.0.0.1:6443:6443/tcp -p 0.0.0.0:80:80 -p 0.0.0.0:443:443 " +
		"--device nvidia.com/gpu=all orcinus/k3s-nvidia:v1.31.5-k3s1 server --write-kubeconfig-mode=644"
	if got != want {
		t.Errorf("createArgs:\n got  %s\n want %s", got, want)
	}
	// 0 drops the port instead of publishing it.
	if a := strings.Join(c.createArgs("orcinus", 0, 0), " "); strings.Contains(a, ":80") {
		t.Errorf("port 0 still published: %s", a)
	}
}
